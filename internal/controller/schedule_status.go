package controller

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// Condition types shared by ScalingGroup and ScalingConfig.
const (
	// ConditionReady is True once every target reached the desired state.
	ConditionReady = "Ready"
	// ConditionManualOverride is True while spec.active (or the legacy annotation)
	// overrides the schedule.
	ConditionManualOverride = "ManualOverride"
)

// settledRequeue is the requeue interval once a target has converged: a minute, or less
// when the next schedule transition is closer, so boundaries take effect on time.
func settledRequeue(d scaling.Decision, now time.Time) time.Duration {
	const maxWait = time.Minute
	if d.NextTransition == nil {
		return maxWait
	}
	wait := d.NextTransition.Sub(now) + time.Second
	if wait < time.Second {
		return time.Second
	}
	return min(wait, maxWait)
}

// applyDecision mirrors a scheduling decision into the status and the ManualOverride
// condition.
func applyDecision(st *finopsv1.ScheduleStatus, conds *[]metav1.Condition, d scaling.Decision, generation int64, kind string) {
	st.Mode = d.Mode
	st.DesiredState = d.State()
	st.OverrideExpiresAt = nil
	if d.OverrideExpiresAt != nil {
		t := metav1.NewTime(*d.OverrideExpiresAt)
		st.OverrideExpiresAt = &t
	}
	st.NextTransition = nil
	if d.NextTransition != nil {
		next := scaling.StateUp
		if d.Active {
			next = scaling.StateDown
		}
		st.NextTransition = &finopsv1.ScheduledTransition{Time: metav1.NewTime(*d.NextTransition), DesiredState: next}
	}

	cond := metav1.Condition{Type: ConditionManualOverride, ObservedGeneration: generation}
	switch d.Mode {
	case scaling.ModeManualUp, scaling.ModeManualDown:
		cond.Status, cond.Reason = metav1.ConditionTrue, d.Mode
		until := "until spec.active is removed"
		if d.OverrideExpiresAt != nil {
			until = "until " + d.OverrideExpiresAt.UTC().Format(time.RFC3339)
		}
		cond.Message = fmt.Sprintf("The %s is forced %s and its schedule is ignored %s.", kind, d.State(), until)
	case scaling.ModeAlwaysOn:
		cond.Status, cond.Reason = metav1.ConditionFalse, "NoSchedule"
		cond.Message = fmt.Sprintf("The %s has no usable schedule and is kept up as a fail-safe.", kind)
	default:
		cond.Status, cond.Reason = metav1.ConditionFalse, "ScheduleInControl"
		cond.Message = "The schedule decides the desired state."
	}
	meta.SetStatusCondition(conds, cond)
}

// setReadyCondition records whether the observed phase matches the desired state.
func setReadyCondition(conds *[]metav1.Condition, phase string, desiredActive bool, generation int64, detail string) {
	want := scaling.PhaseScaledDown
	if desiredActive {
		want = scaling.PhaseScaledUp
	}
	cond := metav1.Condition{Type: ConditionReady, Reason: phase, ObservedGeneration: generation}
	if phase == want {
		cond.Status, cond.Message = metav1.ConditionTrue, "All targets reached the desired state."
	} else {
		cond.Status, cond.Message = metav1.ConditionFalse, detail
		if cond.Message == "" {
			cond.Message = "Scaling is in progress."
		}
	}
	meta.SetStatusCondition(conds, cond)
}
