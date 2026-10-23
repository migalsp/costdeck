package scaling

import (
	"fmt"
	"math"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// history is a week of hourly points ending at end, with the cores fn gives each hour (in loc).
func history(end time.Time, loc *time.Location, fn func(t time.Time) float64) []finopsv1.MetricDataPoint {
	var out []finopsv1.MetricDataPoint
	for t := end.Add(-7 * 24 * time.Hour); !t.After(end); t = t.Add(time.Hour) {
		out = append(out, finopsv1.MetricDataPoint{Timestamp: metav1.NewTime(t), CPU: finopsv1.ResourceMetrics{Usage: fmt.Sprintf("%.3f", fn(t.In(loc)))}})
	}
	return out
}

func TestSuggestScheduleFromWorkingHours(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	end := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) // a Sunday
	// Busy 09:00–17:00 Berlin time on weekdays, a trickle otherwise.
	work := func(t time.Time) float64 {
		if t.Weekday() >= time.Monday && t.Weekday() <= time.Friday && t.Hour() >= 9 && t.Hour() < 17 {
			return 0.8
		}
		return 0.002
	}
	s := SuggestSchedule(history(end, time.UTC, func(t time.Time) float64 { return work(t.In(berlin)) }), 0, berlin)
	if s.Reason != SuggestPattern || s.Days != 7 || s.Timezone != "Europe/Berlin" {
		t.Fatalf("suggestion = %+v", s)
	}
	// One window for the five weekdays, an hour of margin on either side.
	want := []finopsv1.ScalingSchedule{{Days: []int{1, 2, 3, 4, 5}, StartTime: "08:00", EndTime: "18:00", Timezone: "Europe/Berlin"}}
	if fmt.Sprint(s.Schedules) != fmt.Sprint(want) {
		t.Errorf("schedules = %+v, want %+v", s.Schedules, want)
	}
	if math.Abs(s.UpShare-50.0/168) > 1e-9 {
		t.Errorf("up share = %v, want 50/168", s.UpShare)
	}
	// The schedules mean exactly the hours the suggestion keeps up, under the real rules.
	tt := Timetable{Schedules: s.Schedules}
	monday := time.Date(2026, 10, 19, 0, 30, 0, 0, berlin)
	for h := range 168 {
		at := monday.Add(time.Duration(h) * time.Hour)
		if got, want := tt.ActiveAt(at), s.Up[at.Weekday()][at.Hour()]; got != want {
			t.Errorf("%s: schedule up=%v, suggestion up=%v", at.Format("Mon 15:04"), got, want)
		}
	}
}

func TestSuggestScheduleShapes(t *testing.T) {
	end := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		fn     func(time.Time) float64
		reason string
	}{
		"idle":   {func(time.Time) float64 { return 0.001 }, SuggestIdle},
		"steady": {func(time.Time) float64 { return 0.5 }, SuggestSteady},
		// Busy 20 hours a day: a schedule would save too little.
		"busy most of the day": {func(t time.Time) float64 {
			if t.Hour() < 20 {
				return 1
			}
			return 0
		}, SuggestSteady},
	}
	for name, c := range cases {
		if s := SuggestSchedule(history(end, time.UTC, c.fn), 0, time.UTC); s.Reason != c.reason || len(s.Schedules) != 0 {
			t.Errorf("%s: %s with %d schedules, want %s", name, s.Reason, len(s.Schedules), c.reason)
		}
	}
	if s := SuggestSchedule(history(end, time.UTC, func(time.Time) float64 { return 1 })[:10], 0, time.UTC); s.Reason != SuggestInsufficient {
		t.Errorf("ten hours of history: %s", s.Reason)
	}
}

// Days with different hours get schedules of their own; a night shift that crosses
// midnight is split over the two days.
func TestSuggestScheduleSeveralWindows(t *testing.T) {
	end := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	fn := func(t time.Time) float64 {
		switch {
		case t.Weekday() == time.Saturday && t.Hour() >= 10 && t.Hour() < 12:
			return 1
		case t.Weekday() == time.Wednesday && t.Hour() >= 22:
			return 1
		case t.Weekday() == time.Thursday && t.Hour() < 2:
			return 1
		}
		return 0
	}
	s := SuggestSchedule(history(end, time.UTC, fn), 0, time.UTC)
	got := fmt.Sprint(s.Schedules)
	want := fmt.Sprint([]finopsv1.ScalingSchedule{
		{Days: []int{3}, StartTime: "21:00", EndTime: "23:59", Timezone: "UTC"},
		{Days: []int{4}, StartTime: "00:00", EndTime: "03:00", Timezone: "UTC"},
		{Days: []int{6}, StartTime: "09:00", EndTime: "13:00", Timezone: "UTC"},
	})
	if got != want {
		t.Errorf("schedules = %s\nwant %s", got, want)
	}
}

// History points average the hour before them: the point at 10:00 is the hour from 09:00.
func TestSuggestScheduleReadsPointsAsTheStepBeforeThem(t *testing.T) {
	end := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	// Busy 09:00–11:00 every day, reported by the points of 10:00 and 11:00.
	points := history(end, time.UTC, func(t time.Time) float64 {
		if t.Hour() == 10 || t.Hour() == 11 {
			return 1
		}
		return 0
	})
	s := SuggestSchedule(points, time.Hour, time.UTC)
	if len(s.Schedules) != 1 || s.Schedules[0].StartTime != "08:00" || s.Schedules[0].EndTime != "12:00" {
		t.Errorf("schedules = %+v, want 08:00–12:00 (09:00–11:00 with an hour of margin)", s.Schedules)
	}
}
