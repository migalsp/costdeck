package scaling

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

var crdTimePattern = regexp.MustCompile(`^([0-1]?[0-9]|2[0-3]):[0-5][0-9]$`)

var fuzzWeekStart = time.Date(2026, time.October, 25, 0, 0, 0, 0, time.UTC)

func positiveMod(a, n int) int { return ((a % n) + n) % n }

func clockTime(minutes int) string { return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60) }

func weekdaysIn(mask uint8) []int {
	var days []int
	for d := range 8 {
		if mask&(1<<d) != 0 {
			days = append(days, d)
		}
	}
	return days
}

func FuzzParseMinutes(f *testing.F) {
	for _, s := range []string{"07:30", "7:05", "00:00", "23:59", "24:00", "12:60", "", " 08:00 ", "-1:30", "08:00junk", "8:5", "+8:05", "1:2:3"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		minutes, ok := parseMinutes(s)
		if crdTimePattern.MatchString(s) {
			hours, mins, _ := strings.Cut(s, ":")
			h, _ := strconv.Atoi(hours)
			m, _ := strconv.Atoi(mins)
			if !ok || minutes != h*60+m {
				t.Fatalf("parseMinutes(%q) = %d, %v; the CRD accepts it as %d", s, minutes, ok, h*60+m)
			}
		}
		if !ok {
			return
		}
		if minutes < 0 || minutes >= minutesPerDay {
			t.Fatalf("parseMinutes(%q) = %d, outside one day", s, minutes)
		}
		if again, ok := parseMinutes(clockTime(minutes)); !ok || again != minutes {
			t.Fatalf("parseMinutes(%q) = %d, but %q reads back as %d, %v", s, minutes, clockTime(minutes), again, ok)
		}
	})
}

func FuzzScheduleWindow(f *testing.F) {
	f.Add(false, uint8(0b0111110), uint8(0), uint8(0), uint16(8*60), uint16(18*60), uint16(minutesPerDay+12*60))
	f.Add(false, uint8(0b1000000), uint8(0), uint8(0), uint16(22*60), uint16(6*60), uint16(5))
	f.Add(true, uint8(0), uint8(5), uint8(1), uint16(18*60), uint16(8*60), uint16(0))
	f.Add(true, uint8(0), uint8(1), uint8(5), uint16(0), uint16(23*60+59), uint16(6*minutesPerDay))
	f.Add(false, uint8(0b10000000), uint8(0), uint8(0), uint16(9*60), uint16(17*60), uint16(10*60))
	f.Add(false, uint8(0b0000001), uint8(0), uint8(0), uint16(600), uint16(600), uint16(600))
	f.Fuzz(func(t *testing.T, continuous bool, dayMask, startDay, endDay uint8, start, end, probe uint16) {
		startMin, endMin := int(start)%minutesPerDay, int(end)%minutesPerDay
		schedule := finopsv1.ScalingSchedule{StartTime: clockTime(startMin), EndTime: clockTime(endMin), Timezone: "UTC"}
		var opens []int
		var length int
		if continuous {
			sd, ed := int(startDay)%7, int(endDay)%7
			schedule.StartDay, schedule.EndDay = &sd, &ed
			opens = []int{sd*minutesPerDay + startMin}
			length = positiveMod(ed*minutesPerDay+endMin-opens[0], minutesPerWeek)
		} else {
			schedule.Days = weekdaysIn(dayMask)
			for _, d := range schedule.Days {
				if d < 7 {
					opens = append(opens, d*minutesPerDay+startMin)
				}
			}
			length = positiveMod(endMin-startMin, minutesPerDay)
		}
		minute := int(probe) % minutesPerWeek
		want := len(opens) == 0
		for _, open := range opens {
			if positiveMod(minute-open, minutesPerWeek) <= length {
				want = true
			}
		}
		at := fuzzWeekStart.Add(time.Duration(minute) * time.Minute)
		if got := (&Engine{}).IsActiveAt(at, []finopsv1.ScalingSchedule{schedule}, nil); got != want {
			t.Fatalf("%+v at %s: active %v, want %v", schedule, at.Format("Mon 15:04"), got, want)
		}
	})
}

func FuzzTimetableNextChange(f *testing.F) {
	f.Add(uint8(0b0111110), uint16(8*60), uint16(18*60), uint16(0), uint16(0), false, uint32(0))
	f.Add(uint8(0b0111110), uint16(8*60), uint16(18*60), uint16(2*minutesPerDay), uint16(minutesPerDay), false, uint32(36*3600))
	f.Add(uint8(0), uint16(0), uint16(0), uint16(20*minutesPerDay), uint16(3*minutesPerDay), false, uint32(0))
	f.Add(uint8(0b1111111), uint16(0), uint16(23*60+59), uint16(9*minutesPerDay), uint16(60), false, uint32(7))
	f.Add(uint8(0b0000001), uint16(22*60), uint16(6*60), uint16(minutesPerDay-30), uint16(90), true, uint32(23*3600+59*60+59))
	f.Fuzz(func(t *testing.T, dayMask uint8, start, end, exceptionStart, exceptionLength uint16, exceptionUp bool, offsetSeconds uint32) {
		var tt Timetable
		if days := weekdaysIn(dayMask); len(days) > 0 {
			tt.Schedules = []finopsv1.ScalingSchedule{{
				Days:      days,
				StartTime: clockTime(int(start) % minutesPerDay),
				EndTime:   clockTime(int(end) % minutesPerDay),
				Timezone:  "UTC",
			}}
		}
		var probes []time.Time
		if exceptionLength > 0 {
			from := fuzzWeekStart.Add(time.Duration(exceptionStart) * time.Minute)
			to := from.Add(time.Duration(exceptionLength) * time.Minute)
			tt.Exceptions = []Exception{{Name: "fuzz", From: from, To: to, Active: exceptionUp}}
			probes = append(probes, from, to, to.Add(time.Minute))
		}
		now := fuzzWeekStart.Add(time.Duration(offsetSeconds%(14*24*3600)) * time.Second)
		current := tt.ActiveAt(now)
		next := tt.NextChange(now)
		if next == nil {
			for _, later := range []time.Duration{time.Minute, time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 100 * 24 * time.Hour} {
				probes = append(probes, now.Add(later))
			}
			for _, p := range probes {
				if p.After(now) && tt.ActiveAt(p) != current {
					t.Fatalf("NextChange(%s) is nil, but the state is %v at %s instead of %v", now, tt.ActiveAt(p), p, current)
				}
			}
			return
		}
		if !next.After(now) {
			t.Fatalf("NextChange(%s) = %s, not after it", now, next)
		}
		if tt.ActiveAt(*next) == current {
			t.Fatalf("NextChange(%s) = %s, but the state stays %v there", now, next, current)
		}
		for _, before := range []time.Time{next.Add(-time.Second), now.Add(next.Sub(now) / 2)} {
			if before.After(now) && tt.ActiveAt(before) != current {
				t.Fatalf("NextChange(%s) = %s, but the state already changes at %s", now, next, before)
			}
		}
	})
}

func FuzzResolveException(f *testing.F) {
	f.Add("2026-12-24", "", StateDown, "")
	f.Add("2026-12-24T18:00", "2026-12-27", StateUp, "Europe/Berlin")
	f.Add("2026-03-29", "", StateDown, "Europe/Moscow")
	f.Add("2026-10-25T02:30", "2026-10-25T02:15", StateDown, "Europe/Berlin")
	f.Add("2026-12-27", "2026-12-24", StateDown, "UTC")
	f.Add("tomorrow", "", "Sideways", "Mars/Olympus")
	f.Fuzz(func(t *testing.T, start, end, state, zone string) {
		ex, err := resolveException(finopsv1.ScheduleException{Name: "fuzz", Start: start, End: end, State: state, Timezone: zone}, "UTC")
		if err != nil {
			return
		}
		if state != StateUp && state != StateDown {
			t.Fatalf("state %q was accepted", state)
		}
		if ex.Active != (state == StateUp) {
			t.Fatalf("state %q resolved to active %v", state, ex.Active)
		}
		if !ex.To.After(ex.From) {
			t.Fatalf("start %q, end %q resolved to [%s, %s), which is empty", start, end, ex.From, ex.To)
		}
	})
}
