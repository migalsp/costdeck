package scaling

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// officeHours is Monday-Friday 09:00-18:00 UTC; inclusive at both ends, so the group goes
// down at 18:01.
var officeHours = []finopsv1.ScalingSchedule{{
	Days: []int{1, 2, 3, 4, 5}, StartTime: "09:00", EndTime: "18:00", Timezone: "UTC",
}}

func TestDecideScheduleMode(t *testing.T) {
	e := &Engine{}

	d := e.Decide(at(t, time.Wednesday, "12:00"), officeHours, nil, nil, nil)
	if !d.Active || d.Mode != ModeSchedule {
		t.Fatalf("Decide(Wed 12:00) = %+v, want active schedule mode", d)
	}
	want := at(t, time.Wednesday, "18:01")
	if d.NextTransition == nil || !d.NextTransition.Equal(want) {
		t.Errorf("NextTransition = %v, want %v", d.NextTransition, want)
	}

	// Friday evening: the next change is Monday morning, across the weekend.
	d = e.Decide(at(t, time.Friday, "19:30"), officeHours, nil, nil, nil)
	want = at(t, time.Monday, "09:00").AddDate(0, 0, 7)
	if d.Active || d.NextTransition == nil || !d.NextTransition.Equal(want) {
		t.Errorf("Decide(Fri 19:30) = active %v next %v, want down until %v", d.Active, d.NextTransition, want)
	}
}

func TestDecideAlwaysOnWithoutSchedule(t *testing.T) {
	d := (&Engine{}).Decide(time.Now(), nil, nil, nil, nil)
	if !d.Active || d.Mode != ModeAlwaysOn || d.NextTransition != nil {
		t.Errorf("Decide(no schedule) = %+v, want AlwaysOn with no transition", d)
	}
}

func TestDecideOpenEndedOverride(t *testing.T) {
	up := true
	d := (&Engine{}).Decide(at(t, time.Saturday, "03:00"), officeHours, &up, nil, nil)
	if !d.Active || d.Mode != ModeManualUp {
		t.Fatalf("Decide(override up) = %+v", d)
	}
	if d.NextTransition != nil || d.OverrideExpiresAt != nil {
		t.Errorf("an open-ended override never transitions on its own, got next=%v expires=%v", d.NextTransition, d.OverrideExpiresAt)
	}
}

func TestDecideBoundedOverride(t *testing.T) {
	e := &Engine{}
	up := true

	// Forced up on Saturday until Sunday 12:00: the schedule says down then, so that is
	// the transition.
	until := metav1.NewTime(at(t, time.Sunday, "12:00").AddDate(0, 0, 7))
	d := e.Decide(at(t, time.Saturday, "03:00"), officeHours, &up, &until, nil)
	if d.Mode != ModeManualUp || d.NextTransition == nil || !d.NextTransition.Equal(until.Time) {
		t.Errorf("Decide(bounded override) = %+v, want transition at %v", d, until.Time)
	}

	// Forced up until Monday 10:00, when the schedule is up anyway: the next real change
	// is the schedule's 18:01 scale-down.
	until = metav1.NewTime(at(t, time.Monday, "10:00").AddDate(0, 0, 7))
	d = e.Decide(at(t, time.Saturday, "03:00"), officeHours, &up, &until, nil)
	want := at(t, time.Monday, "18:01").AddDate(0, 0, 7)
	if d.NextTransition == nil || !d.NextTransition.Equal(want) {
		t.Errorf("NextTransition = %v, want %v", d.NextTransition, want)
	}

	// An expired override is ignored entirely.
	expired := metav1.NewTime(at(t, time.Wednesday, "11:00"))
	d = e.Decide(at(t, time.Wednesday, "12:00"), officeHours, new(bool), &expired, nil)
	if !d.Active || d.Mode != ModeSchedule {
		t.Errorf("Decide(expired override) = %+v, want the schedule back in control", d)
	}
}

func TestDecideLegacyAnnotationWins(t *testing.T) {
	up := true
	d := (&Engine{}).Decide(at(t, time.Wednesday, "12:00"), officeHours, &up, nil,
		map[string]string{LegacyOverrideAnnotation: "ScaledDown"})
	if d.Active || d.Mode != ModeManualDown {
		t.Errorf("Decide(legacy annotation) = %+v, want forced down", d)
	}
}

func TestNextScheduleChangeAlwaysOpenWindow(t *testing.T) {
	allWeek := []finopsv1.ScalingSchedule{{Days: []int{0, 1, 2, 3, 4, 5, 6}, StartTime: "00:00", EndTime: "23:59", Timezone: "UTC"}}
	if next := (&Engine{}).NextScheduleChange(at(t, time.Monday, "12:00"), allWeek); next != nil {
		t.Errorf("a 24/7 window never changes, got %v", next)
	}
}
