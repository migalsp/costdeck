package scaling

import (
	"fmt"
	"regexp"
	"slices"
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
		if ok != crdTimePattern.MatchString(s) {
			t.Fatalf("parseMinutes(%q) accepted it: %v, but the CRD pattern accepts it: %v", s, ok, !ok)
		}
		if !ok {
			return
		}
		hours, mins, _ := strings.Cut(s, ":")
		h, _ := strconv.Atoi(hours)
		m, _ := strconv.Atoi(mins)
		if minutes != h*60+m {
			t.Fatalf("parseMinutes(%q) = %d, want %d", s, minutes, h*60+m)
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
	f.Add(uint8(0b0111110), uint16(8*60), uint16(18*60), uint16(0), uint16(0), false, uint32(0), uint8(0))
	f.Add(uint8(0b0111110), uint16(8*60), uint16(18*60), uint16(2*minutesPerDay), uint16(minutesPerDay), false, uint32(36*3600), uint8(0))
	f.Add(uint8(0), uint16(0), uint16(0), uint16(20*minutesPerDay), uint16(3*minutesPerDay), false, uint32(0), uint8(0))
	f.Add(uint8(0b1111111), uint16(0), uint16(23*60+59), uint16(9*minutesPerDay), uint16(60), false, uint32(7), uint8(0))
	f.Add(uint8(0b0000001), uint16(22*60), uint16(6*60), uint16(minutesPerDay-30), uint16(90), true, uint32(23*3600+59*60+59), uint8(0))
	f.Add(uint8(0b0111110), uint16(8*60), uint16(18*60), uint16(0), uint16(16*minutesPerDay), false, uint32(36*3600), uint8(0))
	f.Add(uint8(0b0111110), uint16(8*60), uint16(18*60), uint16(0), uint16(16*minutesPerDay), false, uint32(36*3600), uint8(10))
	f.Add(uint8(0b0000001), uint16(0), uint16(5), uint16(minutesPerDay), uint16(30), true, uint32(0), uint8(20))
	f.Fuzz(func(t *testing.T, dayMask uint8, start, end, exceptionStart, exceptionLength uint16, exceptionUp bool, offsetSeconds uint32, leadMinutes uint8) {
		tt := Timetable{Lead: time.Duration(leadMinutes%121) * time.Minute}
		if days := weekdaysIn(dayMask); len(days) > 0 {
			tt.Schedules = []finopsv1.ScalingSchedule{{
				Days:      days,
				StartTime: clockTime(int(start) % minutesPerDay),
				EndTime:   clockTime(int(end) % minutesPerDay),
				Timezone:  "UTC",
			}}
		}
		if exceptionLength > 0 {
			from := fuzzWeekStart.Add(time.Duration(exceptionStart) * time.Minute)
			tt.Exceptions = []Exception{{Name: "fuzz", From: from, To: from.Add(time.Duration(exceptionLength) * time.Minute), Active: exceptionUp}}
		}
		now := fuzzWeekStart.Add(time.Duration(offsetSeconds%(14*24*3600)) * time.Second)
		got, want := tt.NextChange(now), nextChangeAmongCandidates(tt, now)
		if (got == nil) != (want == nil) || (got != nil && !got.Equal(*want)) {
			t.Fatalf("%+v from %s: NextChange = %s, want %s", tt, now, describeTime(got), describeTime(want))
		}
	})
}

func nextChangeAmongCandidates(tt Timetable, now time.Time) *time.Time {
	const week = 7 * 24 * time.Hour
	horizon := now
	var candidates []time.Time
	for _, ex := range tt.Exceptions {
		candidates = append(candidates, ex.From, ex.To)
		if ex.To.After(horizon) {
			horizon = ex.To
		}
	}
	horizon = horizon.Add(week)
	weekly := Timetable{Schedules: tt.Schedules}
	for m := range minutesPerWeek {
		flip := fuzzWeekStart.Add(time.Duration(m) * time.Minute)
		if weekly.ActiveAt(flip) == weekly.ActiveAt(flip.Add(-time.Minute)) {
			continue
		}
		for at := flip; !at.After(horizon.Add(tt.Lead)); at = at.Add(week) {
			candidates = append(candidates, at)
		}
	}
	for _, c := range slices.Clone(candidates) {
		candidates = append(candidates, c.Add(-tt.Lead))
	}
	slices.SortFunc(candidates, func(a, b time.Time) int { return a.Compare(b) })
	current := tt.ActiveAt(now)
	for _, c := range candidates {
		if c.After(now) && !c.After(horizon) && tt.ActiveAt(c) != current {
			return &c
		}
	}
	return nil
}

func describeTime(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Format(time.RFC3339)
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
