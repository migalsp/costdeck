package scaling

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// Modes reported in status.mode. They answer "why is this up (or down) right now?".
const (
	// ModeSchedule: spec.schedules decide.
	ModeSchedule = "Schedule"
	// ModeManualUp / ModeManualDown: a manual override is in force and the schedule is
	// ignored until it expires or is removed.
	ModeManualUp   = "ManualUp"
	ModeManualDown = "ManualDown"
	// ModeAlwaysOn: no usable schedule and no override, so the fail-safe keeps it up.
	ModeAlwaysOn = "AlwaysOn"
	// ModeDependency: a ScalingGroup kept up because a group that depends on it is up.
	ModeDependency = "Dependency"
)

// Desired states reported in status.desiredState.
const (
	StateUp   = "Up"
	StateDown = "Down"
)

// LegacyOverrideAnnotation is the pre-spec.active manual override. It is still honoured so
// that objects pinned by older AI tooling keep their state, but it is surfaced in the
// status and cleared by the "follow schedule" action.
const LegacyOverrideAnnotation = "costdeck.io/manual-override"

// scanHorizon bounds the search for the next schedule change. Every schedule repeats
// weekly, so a change that does not happen within a week never happens.
const scanHorizon = minutesPerWeek + 1

// Decision explains the desired state of a group or config at a point in time.
type Decision struct {
	Active bool
	Mode   string
	// OverrideExpiresAt is set while a time-bounded manual override is in force.
	OverrideExpiresAt *time.Time
	// NextTransition is when Active next changes on its own: a schedule boundary or the
	// expiry of an override. Nil when it never does.
	NextTransition *time.Time
}

// State renders Active as StateUp or StateDown.
func (d Decision) State() string {
	if d.Active {
		return StateUp
	}
	return StateDown
}

// Decide resolves the desired state from the override, its deadline and the schedules.
// annotations may carry the legacy override annotation, which wins over everything else.
func (e *Engine) Decide(now time.Time, schedules []finopsv1.ScalingSchedule, active *bool, activeUntil *metav1.Time, annotations map[string]string) Decision {
	if legacy, ok := legacyOverride(annotations); ok {
		return Decision{Active: legacy, Mode: manualMode(legacy)}
	}

	if override := EffectiveOverride(active, activeUntil, now); override != nil {
		d := Decision{Active: *override, Mode: manualMode(*override)}
		if activeUntil != nil && !activeUntil.IsZero() {
			expiry := activeUntil.Time
			d.OverrideExpiresAt = &expiry
			if e.IsActiveAt(expiry, schedules, nil) != *override {
				d.NextTransition = &expiry
			} else {
				d.NextTransition = e.NextScheduleChange(expiry, schedules)
			}
		}
		return d
	}

	d := Decision{Active: e.IsActiveAt(now, schedules, nil), Mode: ModeSchedule}
	if !hasUsableSchedule(schedules) {
		d.Mode = ModeAlwaysOn
		return d
	}
	d.NextTransition = e.NextScheduleChange(now, schedules)
	return d
}

// NextScheduleChange returns the first minute after now at which the schedules flip the
// desired state, or nil if they never do (for example a 24/7 window).
func (e *Engine) NextScheduleChange(now time.Time, schedules []finopsv1.ScalingSchedule) *time.Time {
	if !hasUsableSchedule(schedules) {
		return nil
	}
	current := e.IsActiveAt(now, schedules, nil)
	t := now.Truncate(time.Minute)
	for range scanHorizon {
		t = t.Add(time.Minute)
		if e.IsActiveAt(t, schedules, nil) != current {
			return &t
		}
	}
	return nil
}

func hasUsableSchedule(schedules []finopsv1.ScalingSchedule) bool {
	for _, s := range schedules {
		if _, ok := parseWindow(s); ok {
			return true
		}
	}
	return false
}

func manualMode(active bool) string {
	if active {
		return ModeManualUp
	}
	return ModeManualDown
}

func legacyOverride(annotations map[string]string) (bool, bool) {
	switch annotations[LegacyOverrideAnnotation] {
	case "ScaledUp":
		return true, true
	case "ScaledDown":
		return false, true
	}
	return false, false
}
