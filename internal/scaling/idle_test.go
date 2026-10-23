package scaling

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func TestApplyIdle(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	later := now.Add(5 * time.Hour)
	earlier := now.Add(-time.Minute)
	tomorrow := now.Add(18 * time.Hour)
	up := Decision{Active: true, Mode: ModeSchedule}
	// By default the schedule ends when the sleep does and starts again tomorrow.
	scheduleDown := func(time.Time) Decision {
		return Decision{Active: false, Mode: ModeSchedule, NextTransition: &tomorrow}
	}
	scheduleUp := func(time.Time) Decision { return up }
	cases := []struct {
		name  string
		d     Decision
		until *time.Time
		at    func(time.Time) Decision
		want  Decision
	}{
		{"asleep until the schedule next starts", up, &later, scheduleDown, Decision{Active: false, Mode: ModeIdle, NextTransition: &tomorrow}},
		{"asleep until the sleep ends, while the schedule stays up", up, &later, scheduleUp, Decision{Active: false, Mode: ModeIdle, NextTransition: &later}},
		{"the sleep is over", up, &earlier, scheduleDown, up},
		{"never asleep", up, nil, scheduleDown, up},
		{"a manual start wins", Decision{Active: true, Mode: ModeManualUp}, &later, scheduleDown, Decision{Active: true, Mode: ModeManualUp}},
		{"an exception wins", Decision{Active: true, Mode: ModeException}, &later, scheduleDown, Decision{Active: true, Mode: ModeException}},
		{"a dependency wins", Decision{Active: true, Mode: ModeDependency}, &later, scheduleDown, Decision{Active: true, Mode: ModeDependency}},
		{"the fail-safe wins", Decision{Active: true, Mode: ModeAlwaysOn}, &later, scheduleDown, Decision{Active: true, Mode: ModeAlwaysOn}},
		{"down by schedule anyway", Decision{Active: false, Mode: ModeSchedule}, &later, scheduleDown, Decision{Active: false, Mode: ModeSchedule}},
	}
	for _, c := range cases {
		got := ApplyIdle(c.d, c.until, now, c.at)
		if got.Active != c.want.Active || got.Mode != c.want.Mode || (c.want.NextTransition != nil && (got.NextTransition == nil || !got.NextTransition.Equal(*c.want.NextTransition))) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestIdleFor(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	h2 := 2 * time.Hour
	cases := []struct {
		name     string
		period   time.Duration
		upSince  time.Time
		requests float64
		known    bool
		want     bool
	}{
		{"unused for the whole period", h2, now.Add(-3 * time.Hour), 0, true, true},
		{"one request", h2, now.Add(-3 * time.Hour), 1, true, false},
		{"up for less than the period", h2, now.Add(-time.Hour), 0, true, false},
		{"traffic unknown", h2, now.Add(-3 * time.Hour), 0, false, false},
		{"switched off", 0, now.Add(-3 * time.Hour), 0, true, false},
		{"never up", h2, time.Time{}, 0, true, false},
	}
	for _, c := range cases {
		if got := IdleFor(c.period, c.upSince, now, c.requests, c.known); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

// A group asleep for being unused is down in the plan, so its dependencies may stop too,
// while a group that depends on it brings it back.
func TestPlanGroupsWithIdleSleep(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC) // a Thursday
	// The schedule's next change, the end of its window at 18:00:59.
	until := metav1.NewTime(now.Add(4*time.Hour + time.Minute))
	office := []finopsv1.ScalingSchedule{{Days: []int{1, 2, 3, 4, 5}, StartTime: "08:00", EndTime: "18:00", Timezone: "UTC"}}
	group := func(name string, deps ...string) finopsv1.ScalingGroup {
		g := finopsv1.ScalingGroup{Spec: finopsv1.ScalingGroupSpec{Schedules: office, DependsOn: deps}}
		g.Name = name
		g.Status.Phase = PhaseScaledUp
		return g
	}
	shop := group("shop", "db")
	shop.Status.IdleUntil = &until
	db := group("db")
	e := &Engine{}

	plans := e.PlanGroups(now, []finopsv1.ScalingGroup{shop, db}, nil)
	if got := plans["shop"].Decision; got.Active || got.Mode != ModeIdle {
		t.Errorf("shop = %+v, want asleep", got)
	}
	// The schedule ends when the sleep does and starts again on Friday at 08:00.
	if next := plans["shop"].Decision.NextTransition; next == nil || !next.Equal(time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("shop wakes at %v, want Friday 08:00", next)
	}
	if got := plans["db"]; strings.Join(got.RequiredBy, ",") != "shop" {
		t.Errorf("db required by %v: shop, on its way down, stops before it", got.RequiredBy)
	}
	shop.Status.Phase = PhaseScaledDown
	plans = e.PlanGroups(now, []finopsv1.ScalingGroup{shop, db}, nil)
	if got := plans["db"]; !got.Decision.Active || got.Decision.Mode != ModeSchedule || len(got.RequiredBy) != 0 {
		t.Errorf("db = %+v required by %v: the sleeping shop must not hold it", got.Decision, got.RequiredBy)
	}

	db.Status.IdleUntil, shop.Status.IdleUntil = &until, nil
	plans = e.PlanGroups(now, []finopsv1.ScalingGroup{shop, db}, nil)
	if got := plans["db"].Decision; !got.Active || got.Mode != ModeDependency {
		t.Errorf("db = %+v, want kept up for shop", got)
	}
}
