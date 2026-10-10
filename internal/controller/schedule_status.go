package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// Condition types shared by ScalingGroup and ScalingConfig.
const (
	// ConditionReady is True once every target reached the desired state.
	ConditionReady = "Ready"
	// ConditionManualOverride is True while spec.active (or the legacy annotation)
	// overrides the schedule.
	ConditionManualOverride = "ManualOverride"
	// ConditionDependenciesReady reports whether every dependsOn group is ScaledUp.
	ConditionDependenciesReady = "DependenciesReady"
	// ConditionNamespaceConflict is True when the group skips namespaces that an older
	// group already manages.
	ConditionNamespaceConflict = "NamespaceConflict"
)

// updateStatus writes an object's status unless the reconcile left it as it was found:
// a settled schedule would otherwise send an identical status every minute.
func updateStatus(ctx context.Context, c client.Client, obj client.Object, before, after any) error {
	if equality.Semantic.DeepEqual(before, after) {
		return nil
	}
	return c.Status().Update(ctx, obj)
}

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
	case scaling.ModeDependency:
		cond.Status, cond.Reason = metav1.ConditionFalse, "RequiredByDependents"
		cond.Message = fmt.Sprintf("The %s is kept up for the groups that depend on it (status.requiredBy).", kind)
	case scaling.ModeOnDemand:
		cond.Status, cond.Reason = metav1.ConditionFalse, "OnDemand"
		cond.Message = fmt.Sprintf("The %s only runs while a group that depends on it needs it.", kind)
	case scaling.ModeAlwaysOn:
		cond.Status, cond.Reason = metav1.ConditionFalse, "NoSchedule"
		cond.Message = fmt.Sprintf("The %s has no usable schedule and is kept up as a fail-safe.", kind)
	default:
		cond.Status, cond.Reason = metav1.ConditionFalse, "ScheduleInControl"
		cond.Message = "The schedule decides the desired state."
	}
	meta.SetStatusCondition(conds, cond)
}

// recordSavings stores what the kept-down requests would cost per hour and returns the
// amount (zero without a pricing resolver).
func recordSavings(ctx context.Context, st *finopsv1.ScheduleStatus, resolver *pricing.Resolver, cpu, mem resource.Quantity) float64 {
	st.EstimatedHourlySavings, st.Currency = "", ""
	if resolver == nil {
		return 0
	}
	rates := resolver.Rates(ctx)
	hourly := rates.Hourly(cpu, mem)
	if hourly > 0 {
		st.EstimatedHourlySavings = strconv.FormatFloat(hourly, 'f', 4, 64)
		st.Currency = rates.Currency
	}
	return hourly
}

// Notifier announces finished scaling transitions, for example in a Webex space.
type Notifier interface {
	Notify(ctx context.Context, markdown string)
}

// transitionLog remembers the settled phase (ScaledUp or ScaledDown) last announced for
// each object, so that only real changes reach the chat. A group that is up and whose pod
// restarts goes ScaledUp, ScalingUp, ScaledUp; announcing every return would post "is up"
// again after each crash. It lives in memory on the leader: after a restart it learns an
// object's settled phase from its next reconcile without announcing it.
type transitionLog struct {
	mu   sync.Mutex
	last map[types.UID]string
}

func settled(phase string) bool {
	return phase == scaling.PhaseScaledUp || phase == scaling.PhaseScaledDown
}

// changed records a reconcile's phase change and reports whether it is worth announcing:
// the object has just settled in a phase other than the one last announced. The first
// reconcile of a new object (no previous phase) is not announced.
func (l *transitionLog) changed(uid types.UID, oldPhase, newPhase string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[types.UID]string{}
	}
	if _, known := l.last[uid]; !known && settled(oldPhase) {
		l.last[uid] = oldPhase // learnt after a restart: it was announced back then, if at all
	}
	if !settled(newPhase) {
		return false
	}
	last := l.last[uid]
	l.last[uid] = newPhase
	return oldPhase != "" && oldPhase != newPhase && last != newPhase
}

// announce notifies when an object has just settled in a new phase (see changed).
func (l *transitionLog) announce(ctx context.Context, n Notifier, uid types.UID, kind, name, oldPhase, newPhase string, st finopsv1.ScheduleStatus, requiredBy []string) {
	if n == nil || !l.changed(uid, oldPhase, newPhase) {
		return
	}
	go n.Notify(context.WithoutCancel(ctx), transitionMessage(kind, name, newPhase, st, requiredBy))
}

func transitionMessage(kind, name, phase string, st finopsv1.ScheduleStatus, requiredBy []string) string {
	icon, state := "▶️", "is up"
	if phase == scaling.PhaseScaledDown {
		icon, state = "⏸️", "is scaled down"
	}
	why := ""
	switch st.Mode {
	case scaling.ModeSchedule:
		why = "by its schedule"
	case scaling.ModeManualUp, scaling.ModeManualDown:
		why = "by a manual override"
		if st.OverrideExpiresAt != nil {
			why += " until " + st.OverrideExpiresAt.UTC().Format("Mon 15:04 MST")
		}
	case scaling.ModeDependency:
		why = "for " + strings.Join(requiredBy, ", ")
	case scaling.ModeOnDemand:
		why = "because nothing needs it"
	case scaling.ModeAlwaysOn:
		why = "(no schedule)"
	}
	msg := fmt.Sprintf("%s %s `%s` %s %s.", icon, kind, name, state, why)
	if st.NextTransition != nil {
		msg += fmt.Sprintf(" Next change: %s at %s.", strings.ToLower(st.NextTransition.DesiredState), st.NextTransition.Time.UTC().Format("Mon 15:04 MST"))
	}
	if st.EstimatedHourlySavings != "" {
		msg += fmt.Sprintf(" Saving ~%s %s/h.", strings.TrimRight(strings.TrimRight(st.EstimatedHourlySavings, "0"), "."), st.Currency)
	}
	return msg
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
