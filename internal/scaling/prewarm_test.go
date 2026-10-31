package scaling

import (
	"testing"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func TestPrewarmLead(t *testing.T) {
	wakeUps := func(seconds ...int32) []finopsv1.WakeUp {
		out := make([]finopsv1.WakeUp, 0, len(seconds))
		for _, s := range seconds {
			out = append(out, finopsv1.WakeUp{Seconds: s})
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		prewarm *finopsv1.Prewarm
		recent  []finopsv1.WakeUp
		want    time.Duration
	}{
		{"no prewarm", nil, wakeUps(300), 0},
		{"a fixed lead", &finopsv1.Prewarm{Minutes: 10}, wakeUps(300), 10 * time.Minute},
		{"auto before the first wake-up uses the minutes", &finopsv1.Prewarm{Auto: true, Minutes: 5}, nil, 5 * time.Minute},
		{"auto takes the slowest wake-up, rounded up, plus a minute", &finopsv1.Prewarm{Auto: true}, wakeUps(70, 150, 90), 4 * time.Minute},
		{"a wake-up of whole minutes is not rounded further", &finopsv1.Prewarm{Auto: true}, wakeUps(120), 3 * time.Minute},
		{"auto stops at half an hour", &finopsv1.Prewarm{Auto: true}, wakeUps(3600), 30 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PrewarmLead(tc.prewarm, tc.recent); got != tc.want {
				t.Errorf("PrewarmLead = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrewarmStartsAheadOfTheSchedule(t *testing.T) {
	utc := func(d, h, m int) time.Time { return time.Date(2026, 12, d, h, m, 0, 0, time.UTC) }
	officeHours := []finopsv1.ScalingSchedule{{Days: []int{1, 2, 3, 4, 5}, StartTime: "08:00", EndTime: "18:00", Timezone: "UTC"}}
	tt := Timetable{Schedules: officeHours, Lead: 10 * time.Minute}
	e := &Engine{}

	if tt.ActiveAt(utc(7, 7, 49)) || !tt.ActiveAt(utc(7, 7, 50)) || !tt.ActiveAt(utc(7, 18, 0)) || tt.ActiveAt(utc(7, 18, 1)) {
		t.Error("the lead starts the window ten minutes early and leaves its end alone")
	}
	d := e.Decide(utc(7, 7, 55), tt, nil, nil, nil)
	if !d.Active || d.Mode != ModePrewarm || d.PrewarmLead != 10*time.Minute || d.NextTransition == nil || !d.NextTransition.Equal(utc(7, 18, 1)) {
		t.Errorf("decision at 07:55 = %+v, want up in Prewarm until 18:01", d)
	}
	if d := e.Decide(utc(7, 8, 5), tt, nil, nil, nil); d.Mode != ModeSchedule {
		t.Errorf("decision at 08:05 = %+v, want the schedule in control", d)
	}
	if next := tt.NextChange(utc(6, 12, 0)); next == nil || !next.Equal(utc(7, 7, 50)) {
		t.Errorf("next change on Sunday = %v, want Monday 07:50", next)
	}

	holiday := []Exception{{Name: "Holiday", From: utc(4, 0, 0), To: utc(7, 8, 0), Active: false}}
	withHoliday := Timetable{Schedules: officeHours, Exceptions: holiday, Lead: 10 * time.Minute}
	if d := e.Decide(utc(7, 7, 52), withHoliday, nil, nil, nil); !d.Active || d.Mode != ModePrewarm {
		t.Errorf("decision at 07:52 = %+v, want prewarm before office hours even in the last minutes of a holiday", d)
	}
	if next := withHoliday.NextChange(utc(5, 12, 0)); next == nil || !next.Equal(utc(7, 7, 50)) {
		t.Errorf("next change during the holiday = %v, want Monday 07:50", next)
	}

	intervals := tt.Intervals(utc(7, 7, 0), utc(7, 9, 0))
	reasons := make([]string, 0, len(intervals))
	for _, iv := range intervals {
		reasons = append(reasons, iv.Reason+" "+iv.From.Format("15:04")+"-"+iv.To.Format("15:04"))
	}
	if want := []string{"schedule 07:00-07:50", "prewarm 07:50-08:00", "schedule 08:00-09:00"}; len(reasons) != 3 || reasons[0] != want[0] || reasons[1] != want[1] || reasons[2] != want[2] {
		t.Errorf("intervals = %q, want %q", reasons, want)
	}

	if d := e.Decide(utc(7, 7, 55), Timetable{Schedules: officeHours}, nil, nil, nil); d.Active || d.PrewarmLead != 0 {
		t.Errorf("without a lead nothing starts early, got %+v", d)
	}
}
