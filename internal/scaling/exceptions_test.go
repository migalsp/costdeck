package scaling

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestResolveExceptions(t *testing.T) {
	berlin := mustZone(t, "Europe/Berlin")
	day := func(y int, m time.Month, d, h, min int, loc *time.Location) time.Time {
		return time.Date(y, m, d, h, min, 0, 0, loc)
	}
	cases := []struct {
		name     string
		own      []finopsv1.ScheduleException
		names    []string
		cals     []finopsv1.HolidayCalendar
		zone     string
		want     []Exception
		problems []string
	}{
		{
			name: "a date covers the whole day in the schedule's zone",
			own:  []finopsv1.ScheduleException{{Name: "Christmas", Start: "2026-12-25", State: "Down"}},
			zone: "Europe/Berlin",
			want: []Exception{{Name: "Christmas", From: day(2026, 12, 25, 0, 0, berlin), To: day(2026, 12, 26, 0, 0, berlin)}},
		},
		{
			name: "an end date includes that whole day",
			own:  []finopsv1.ScheduleException{{Start: "2026-12-24", End: "2027-01-01", State: "Down"}},
			zone: "UTC",
			want: []Exception{{Name: "2026-12-24", From: day(2026, 12, 24, 0, 0, time.UTC), To: day(2027, 1, 2, 0, 0, time.UTC)}},
		},
		{
			name: "date and time bounds are exact, and the exception's own zone wins",
			own:  []finopsv1.ScheduleException{{Name: "Release", Start: "2026-11-07T06:00", End: "2026-11-08T22:30", State: "Up", Timezone: "Europe/Berlin"}},
			zone: "UTC",
			want: []Exception{{Name: "Release", From: day(2026, 11, 7, 6, 0, berlin), To: day(2026, 11, 8, 22, 30, berlin), Active: true}},
		},
		{
			name: "a start time without an end lasts to the end of that day",
			own:  []finopsv1.ScheduleException{{Name: "Demo", Start: "2026-11-03T14:00", State: "Up"}},
			zone: "UTC",
			want: []Exception{{Name: "Demo", From: day(2026, 11, 3, 14, 0, time.UTC), To: day(2026, 11, 4, 0, 0, time.UTC), Active: true}},
		},
		{
			name:  "calendar days are down by default and come before the object's own exceptions",
			own:   []finopsv1.ScheduleException{{Name: "Hotfix", Start: "2026-12-25T10:00", End: "2026-12-25T12:00", State: "Up"}},
			names: []string{"de"},
			cals: []finopsv1.HolidayCalendar{{Name: "de", Timezone: "Europe/Berlin", Days: []finopsv1.CalendarDay{
				{Date: "2026-12-25", Name: "Christmas Day"}, {Date: "2026-12-26"},
			}}},
			zone: "UTC",
			want: []Exception{
				{Name: "Christmas Day", From: day(2026, 12, 25, 0, 0, berlin), To: day(2026, 12, 26, 0, 0, berlin)},
				{Name: "2026-12-26", From: day(2026, 12, 26, 0, 0, berlin), To: day(2026, 12, 27, 0, 0, berlin)},
				{Name: "Hotfix", From: day(2026, 12, 25, 10, 0, time.UTC), To: day(2026, 12, 25, 12, 0, time.UTC), Active: true},
			},
		},
		{
			name:  "a calendar can keep targets up",
			names: []string{"release-days"},
			cals:  []finopsv1.HolidayCalendar{{Name: "release-days", State: "Up", Days: []finopsv1.CalendarDay{{Date: "2026-11-07"}}}},
			zone:  "UTC",
			want:  []Exception{{Name: "2026-11-07", From: day(2026, 11, 7, 0, 0, time.UTC), To: day(2026, 11, 8, 0, 0, time.UTC), Active: true}},
		},
		{
			name: "what cannot be applied is reported and the rest still applies",
			own: []finopsv1.ScheduleException{
				{Name: "backwards", Start: "2026-12-25", End: "2026-12-20", State: "Down"},
				{Name: "nowhere", Start: "2026-12-25", State: "Down", Timezone: "Mars/Olympus"},
				{Name: "garbled", Start: "25.12.2026", State: "Down"},
				{Name: "sideways", Start: "2026-12-25", State: "Sideways"},
				{Name: "ok", Start: "2026-12-31", State: "Down"},
			},
			names: []string{"missing"},
			zone:  "UTC",
			want:  []Exception{{Name: "ok", From: day(2026, 12, 31, 0, 0, time.UTC), To: day(2027, 1, 1, 0, 0, time.UTC)}},
			problems: []string{`calendar "missing" does not exist`, `"backwards": ends before it starts`, `"nowhere": unknown timezone`,
				`"garbled": start "25.12.2026" is not a date`, `"sideways": state must be Up or Down`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, problems := ResolveExceptions(c.own, c.names, c.cals, c.zone)
			if len(got) != len(c.want) {
				t.Fatalf("got %d exceptions %+v, want %d", len(got), got, len(c.want))
			}
			for i := range got {
				g, w := got[i], c.want[i]
				if g.Name != w.Name || !g.From.Equal(w.From) || !g.To.Equal(w.To) || g.Active != w.Active {
					t.Errorf("exception %d = %+v, want %+v", i, g, w)
				}
			}
			if len(problems) != len(c.problems) {
				t.Fatalf("problems = %q, want %d like %q", problems, len(c.problems), c.problems)
			}
			for i, p := range c.problems {
				if !strings.Contains(problems[i], p) {
					t.Errorf("problem %d = %q, want it to contain %q", i, problems[i], p)
				}
			}
		})
	}
}

func TestExceptionZone(t *testing.T) {
	if z := ExceptionZone(nil); z != defaultExceptionZone {
		t.Errorf("no schedules: %q", z)
	}
	if z := ExceptionZone([]finopsv1.ScalingSchedule{{}, {Timezone: "Asia/Tokyo"}}); z != "Asia/Tokyo" {
		t.Errorf("first schedule with a zone: %q", z)
	}
}

// A whole-day exception follows the local calendar, also on the day clocks go back.
func TestExceptionAcrossDSTChange(t *testing.T) {
	berlin := mustZone(t, "Europe/Berlin")
	ex, problems := ResolveExceptions([]finopsv1.ScheduleException{{Start: "2026-10-25", State: "Down"}}, nil, nil, "Europe/Berlin")
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	if got := ex[0].To.Sub(ex[0].From); got != 25*time.Hour {
		t.Errorf("2026-10-25 in Berlin lasts %v, want 25h", got)
	}
	tt := Timetable{Schedules: alwaysOn, Exceptions: ex}
	if tt.ActiveAt(time.Date(2026, 10, 25, 23, 30, 0, 0, berlin)) {
		t.Error("23:30 local on the exception day should be down")
	}
	if !tt.ActiveAt(time.Date(2026, 10, 26, 0, 0, 0, 0, berlin)) {
		t.Error("midnight after the exception day should be up again")
	}
}

// alwaysOn is a schedule that is up all week, so only exceptions bring targets down.
var alwaysOn = []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "23:59", Timezone: "UTC"}}

func TestDecideWithExceptions(t *testing.T) {
	utc := func(d, h, m int) time.Time { return time.Date(2026, 12, d, h, m, 0, 0, time.UTC) }
	officeHoursUTC := []finopsv1.ScalingSchedule{{Days: []int{1, 2, 3, 4, 5}, StartTime: "08:00", EndTime: "20:00", Timezone: "UTC"}}
	holiday := func(start, end string) []finopsv1.ScheduleException {
		return []finopsv1.ScheduleException{{Name: "Holiday", Start: start, End: end, State: "Down"}}
	}
	resolve := func(own []finopsv1.ScheduleException) []Exception {
		ex, problems := ResolveExceptions(own, nil, nil, defaultExceptionZone)
		if len(problems) > 0 {
			t.Fatal(problems)
		}
		return ex
	}
	e := &Engine{}
	yes, no := true, false

	t.Run("a Down exception beats the schedule and ends on time", func(t *testing.T) {
		// Thursday 2026-12-24 12:00 is inside office hours.
		d := e.Decide(utc(24, 12, 0), Timetable{Schedules: officeHoursUTC, Exceptions: resolve(holiday("2026-12-24", "2026-12-25"))}, nil, nil, nil)
		if d.Active || d.Mode != ModeException || d.Exception != "Holiday" {
			t.Fatalf("decision = %+v", d)
		}
		if !d.ExceptionEndsAt.Equal(utc(26, 0, 0)) {
			t.Errorf("ends at %v", d.ExceptionEndsAt)
		}
		// After the holiday (Saturday) office hours resume on Monday 28th.
		if d.NextTransition == nil || !d.NextTransition.Equal(utc(28, 8, 0)) {
			t.Errorf("next transition = %v, want Mon 28 08:00", d.NextTransition)
		}
	})
	t.Run("a manual override beats the exception", func(t *testing.T) {
		d := e.Decide(utc(24, 12, 0), Timetable{Schedules: officeHoursUTC, Exceptions: resolve(holiday("2026-12-24", ""))}, &yes, nil, nil)
		if !d.Active || d.Mode != ModeManualUp {
			t.Fatalf("decision = %+v", d)
		}
	})
	t.Run("an override that expires inside an exception hands over to it", func(t *testing.T) {
		until := metav1.NewTime(utc(24, 14, 0))
		d := e.Decide(utc(24, 12, 0), Timetable{Schedules: officeHoursUTC, Exceptions: resolve(holiday("2026-12-24", ""))}, &yes, &until, nil)
		if d.NextTransition == nil || !d.NextTransition.Equal(until.Time) {
			t.Errorf("next transition = %v, want the expiry, when the holiday takes the target down", d.NextTransition)
		}
	})
	t.Run("the exception that began last wins an overlap", func(t *testing.T) {
		ex := resolve([]finopsv1.ScheduleException{
			{Name: "Break", Start: "2026-12-21", End: "2027-01-01", State: "Down"},
			{Name: "Release", Start: "2026-12-23T06:00", End: "2026-12-23T22:00", State: "Up"},
		})
		d := e.Decide(utc(23, 12, 0), Timetable{Schedules: officeHoursUTC, Exceptions: ex}, nil, nil, nil)
		if !d.Active || d.Exception != "Release" {
			t.Fatalf("decision = %+v", d)
		}
		if d.NextTransition == nil || !d.NextTransition.Equal(utc(23, 22, 0)) {
			t.Errorf("next transition = %v, want the end of the release window", d.NextTransition)
		}
	})
	t.Run("an exception that does not change the state is no transition", func(t *testing.T) {
		// Down on Saturday is what the schedule does anyway; the next change is Monday 08:00.
		d := e.Decide(utc(26, 10, 0), Timetable{Schedules: officeHoursUTC, Exceptions: resolve(holiday("2026-12-26", ""))}, nil, nil, nil)
		if d.NextTransition == nil || !d.NextTransition.Equal(utc(28, 8, 0)) {
			t.Errorf("next transition = %v", d.NextTransition)
		}
	})
	t.Run("an exception weeks away is the next transition of a target without a schedule", func(t *testing.T) {
		d := e.Decide(utc(1, 9, 0), Timetable{Exceptions: resolve(holiday("2026-12-24", "2026-12-26"))}, nil, nil, nil)
		if !d.Active || d.Mode != ModeAlwaysOn {
			t.Fatalf("decision = %+v", d)
		}
		if d.NextTransition == nil || !d.NextTransition.Equal(utc(24, 0, 0)) {
			t.Errorf("next transition = %v, want the holiday three weeks later", d.NextTransition)
		}
	})
	t.Run("an exception weeks away follows a schedule that is up all week", func(t *testing.T) {
		// 00:00-23:59 every day leaves no gap: both ends of a window are included.
		d := e.Decide(utc(1, 9, 0), Timetable{Schedules: alwaysOn, Exceptions: resolve(holiday("2026-12-24", ""))}, nil, nil, nil)
		if d.NextTransition == nil || !d.NextTransition.Equal(utc(24, 0, 0)) {
			t.Errorf("next transition = %v, want the holiday three weeks later", d.NextTransition)
		}
	})
	t.Run("without schedules or exceptions nothing ever changes", func(t *testing.T) {
		if d := e.Decide(utc(1, 9, 0), Timetable{}, nil, nil, nil); d.NextTransition != nil || d.Mode != ModeAlwaysOn {
			t.Errorf("decision = %+v", d)
		}
	})
	t.Run("a forced down still wins over an Up exception", func(t *testing.T) {
		up := resolve([]finopsv1.ScheduleException{{Start: "2026-12-24", State: "Up"}})
		if d := e.Decide(utc(24, 3, 0), Timetable{Schedules: officeHoursUTC, Exceptions: up}, &no, nil, nil); d.Active {
			t.Errorf("decision = %+v", d)
		}
	})
}

func TestPlanGroupsAppliesCalendars(t *testing.T) {
	cal := []finopsv1.HolidayCalendar{{Name: "us", Days: []finopsv1.CalendarDay{{Date: "2026-11-26", Name: "Thanksgiving"}}}}
	group := func(name, activation string, calendars ...string) finopsv1.ScalingGroup {
		return finopsv1.ScalingGroup{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       finopsv1.ScalingGroupSpec{Schedules: alwaysOn, Calendars: calendars, Activation: activation},
		}
	}
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	plans := (&Engine{}).PlanGroups(now, []finopsv1.ScalingGroup{group("shop", "", "us"), group("typo", "", "usa")}, cal)
	if p := plans["shop"]; p.Decision.Active || p.Decision.Exception != "Thanksgiving" || len(p.ExceptionProblems) > 0 {
		t.Errorf("shop = %+v", p)
	}
	if p := plans["typo"]; !p.Decision.Active || len(p.ExceptionProblems) != 1 {
		t.Errorf("a missing calendar should be reported and ignored: %+v", p)
	}

	upCal := []finopsv1.HolidayCalendar{{Name: "demo", State: "Up", Days: []finopsv1.CalendarDay{{Date: "2026-11-26"}}}}
	onDemand := (&Engine{}).PlanGroups(now, []finopsv1.ScalingGroup{group("db", finopsv1.ActivationOnDemand, "demo")}, upCal)["db"]
	if !onDemand.Decision.Active || onDemand.Decision.Mode != ModeException {
		t.Errorf("an Up day keeps an on-demand group up: %+v", onDemand.Decision)
	}
	downCal := []finopsv1.HolidayCalendar{{Name: "off", Days: []finopsv1.CalendarDay{{Date: "2026-11-26"}}}}
	if d := (&Engine{}).PlanGroups(now, []finopsv1.ScalingGroup{group("db", finopsv1.ActivationOnDemand, "off")}, downCal)["db"].Decision; d.Mode != ModeOnDemand {
		t.Errorf("a Down day leaves an on-demand group on demand: %+v", d)
	}
}

func TestIntervals(t *testing.T) {
	utc := func(d, h, m int) time.Time { return time.Date(2026, 12, d, h, m, 0, 0, time.UTC) }
	office := []finopsv1.ScalingSchedule{{Days: []int{1, 2, 3, 4, 5}, StartTime: "08:00", EndTime: "20:00", Timezone: "UTC"}}
	ex, _ := ResolveExceptions([]finopsv1.ScheduleException{
		{Name: "Christmas Eve", Start: "2026-12-24", State: "Down"},
		{Name: "Boxing Day", Start: "2026-12-26", State: "Down"}, // a Saturday: down anyway
	}, nil, nil, defaultExceptionZone)
	tt := Timetable{Schedules: office, Exceptions: ex}

	// Wednesday 23rd 12:00 to Monday 28th 12:00.
	got := tt.Intervals(utc(23, 12, 0), utc(28, 12, 0))
	want := []Interval{
		{From: utc(23, 12, 0), To: utc(23, 20, 1), Active: true, Reason: "schedule"},
		{From: utc(23, 20, 1), To: utc(24, 0, 0), Active: false, Reason: "schedule"},
		{From: utc(24, 0, 0), To: utc(25, 0, 0), Active: false, Reason: "Christmas Eve"},
		{From: utc(25, 0, 0), To: utc(25, 8, 0), Active: false, Reason: "schedule"},
		{From: utc(25, 8, 0), To: utc(25, 20, 1), Active: true, Reason: "schedule"},
		{From: utc(25, 20, 1), To: utc(26, 0, 0), Active: false, Reason: "schedule"},
		{From: utc(26, 0, 0), To: utc(27, 0, 0), Active: false, Reason: "Boxing Day"},
		{From: utc(27, 0, 0), To: utc(28, 8, 0), Active: false, Reason: "schedule"},
		{From: utc(28, 8, 0), To: utc(28, 12, 0), Active: true, Reason: "schedule"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d intervals:\n%+v", len(got), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if !g.From.Equal(w.From) || !g.To.Equal(w.To) || g.Active != w.Active || g.Reason != w.Reason {
			t.Errorf("interval %d = %+v, want %+v", i, g, w)
		}
	}

	if share := (Timetable{}).ActiveShare(utc(1, 0, 0), utc(8, 0, 0)); share != 1 {
		t.Errorf("no schedule is up all the time, got %v", share)
	}
	// Five 12-hour days and a minute (both ends of a window count) out of a week.
	if share := (Timetable{Schedules: office}).ActiveShare(utc(6, 0, 0), utc(13, 0, 0)); share < 0.357 || share > 0.358 {
		t.Errorf("office hours share = %v, want about 60h/168h", share)
	}
	if got := (Timetable{}).Intervals(utc(1, 0, 0), utc(1, 0, 0)); len(got) != 0 {
		t.Errorf("an empty range has no intervals: %+v", got)
	}
}
