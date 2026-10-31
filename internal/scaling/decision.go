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
	// ModeException: a dated exception or a calendar day (spec.exceptions,
	// spec.calendars) overrides the schedules.
	ModeException = "Exception"
	// ModeAlwaysOn: no usable schedule and no override, so the fail-safe keeps it up.
	ModeAlwaysOn = "AlwaysOn"
	// ModeDependency: a ScalingGroup kept up because a group that depends on it is up.
	ModeDependency = "Dependency"
	ModePrewarm    = "Prewarm"
)

const ReasonPrewarm = "prewarm"

const (
	prewarmMargin  = time.Minute
	maxAutoPrewarm = 30 * time.Minute
)

func PrewarmLead(p *finopsv1.Prewarm, recent []finopsv1.WakeUp) time.Duration {
	if p == nil {
		return 0
	}
	fixed := time.Duration(p.Minutes) * time.Minute
	if !p.Auto || len(recent) == 0 {
		return fixed
	}
	slowest := time.Duration(0)
	for _, w := range recent {
		slowest = max(slowest, time.Duration(w.Seconds)*time.Second)
	}
	return min((slowest+time.Minute-1).Truncate(time.Minute)+prewarmMargin, maxAutoPrewarm)
}

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
	// NextTransition is when Active next changes on its own: a schedule boundary, the
	// start or end of an exception, or the expiry of an override. Nil when it never does.
	NextTransition *time.Time
	// Exception names the exception in force while Mode is ModeException, and
	// ExceptionEndsAt is when it stops applying.
	Exception       string
	ExceptionEndsAt *time.Time
	PrewarmLead     time.Duration
}

// State renders Active as StateUp or StateDown.
func (d Decision) State() string {
	if d.Active {
		return StateUp
	}
	return StateDown
}

// Decide resolves the desired state from the override, its deadline and the timetable
// (schedules and exceptions), in that order. annotations may carry the legacy override
// annotation, which wins over everything else.
func (e *Engine) Decide(now time.Time, tt Timetable, active *bool, activeUntil *metav1.Time, annotations map[string]string) Decision {
	d := e.decide(now, tt, active, activeUntil, annotations)
	d.PrewarmLead = tt.Lead
	return d
}

func (e *Engine) decide(now time.Time, tt Timetable, active *bool, activeUntil *metav1.Time, annotations map[string]string) Decision {
	if legacy, ok := legacyOverride(annotations); ok {
		return Decision{Active: legacy, Mode: manualMode(legacy)}
	}

	if override := EffectiveOverride(active, activeUntil, now); override != nil {
		d := Decision{Active: *override, Mode: manualMode(*override)}
		if activeUntil != nil && !activeUntil.IsZero() {
			expiry := activeUntil.Time
			d.OverrideExpiresAt = &expiry
			if tt.ActiveAt(expiry) != *override {
				d.NextTransition = &expiry
			} else {
				d.NextTransition = tt.NextChange(expiry)
			}
		}
		return d
	}

	if tt.prewarming(now) {
		return Decision{Active: true, Mode: ModePrewarm, NextTransition: tt.NextChange(now)}
	}

	if ex := tt.exceptionAt(now); ex != nil {
		ends := ex.To
		return Decision{Active: ex.Active, Mode: ModeException, Exception: ex.Name, ExceptionEndsAt: &ends, NextTransition: tt.NextChange(now)}
	}

	d := Decision{Active: tt.ActiveAt(now), Mode: ModeSchedule, NextTransition: tt.NextChange(now)}
	if !hasUsableSchedule(tt.Schedules) {
		// Kept up as a fail-safe; only an upcoming exception changes that.
		d.Mode = ModeAlwaysOn
	}
	return d
}

// NextScheduleChange returns the first minute after now at which the schedules flip the
// desired state, or nil if they never do (for example a 24/7 window).
func (e *Engine) NextScheduleChange(now time.Time, schedules []finopsv1.ScalingSchedule) *time.Time {
	return Timetable{Schedules: schedules}.NextChange(now)
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
