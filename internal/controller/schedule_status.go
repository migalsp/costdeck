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
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
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
	// ConditionExceptionsApplied is False while an exception or calendar cannot be applied
	// (a calendar that does not exist, an end before the start). It is absent otherwise.
	ConditionExceptionsApplied = "ExceptionsApplied"
	// ConditionDryRun is True while spec.dryRun is set and says what would change.
	ConditionDryRun = "DryRun"
)

// wouldChange counts the workloads and hibernated services a schedule would change in a
// namespace now.
func wouldChange(ctx context.Context, engine *scaling.Engine, ns string, active bool, exclusions []string, hibernate []finopsv1.HibernateRule) (int, error) {
	n, err := engine.WouldChange(ctx, ns, active, exclusions)
	if err != nil {
		return 0, err
	}
	services, err := engine.ServicesToChange(ctx, ns, active, hibernate)
	return n + services, err
}

// dryRunMessage says what a schedule would change now, for the DryRun condition.
func dryRunMessage(kind string, active bool, workloads, namespaces, cloud int, waitingFor []string) string {
	verb := "scale down"
	if active {
		verb = "start"
	}
	var parts []string
	if workloads > 0 {
		parts = append(parts, fmt.Sprintf("%s %d workload(s) in %d namespace(s)", verb, workloads, namespaces))
	}
	if cloud > 0 {
		parts = append(parts, fmt.Sprintf("%s %d cloud resource(s)", verb, cloud))
	}
	msg := "Nothing to change: everything is already " + map[bool]string{true: "up", false: "down"}[active] + "."
	if len(parts) > 0 {
		msg = "Would " + strings.Join(parts, " and ") + " now."
	}
	if len(waitingFor) > 0 {
		msg += " It would first wait for " + strings.Join(waitingFor, ", ") + "."
	}
	return msg + fmt.Sprintf(" Nothing is scaled while the %s's spec.dryRun is true.", kind)
}

// setDryRunCondition records a dry run; with message "" it removes the condition.
func setDryRunCondition(conds *[]metav1.Condition, message string, generation int64) {
	if message == "" {
		meta.RemoveStatusCondition(conds, ConditionDryRun)
		return
	}
	meta.SetStatusCondition(conds, metav1.Condition{Type: ConditionDryRun, Status: metav1.ConditionTrue, Reason: "DryRun", ObservedGeneration: generation, Message: message})
	meta.SetStatusCondition(conds, metav1.Condition{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: "DryRun", ObservedGeneration: generation, Message: "Nothing is scaled in a dry run."})
}

// setExceptionsCondition reports exceptions that cannot be applied; the others still do.
func setExceptionsCondition(conds *[]metav1.Condition, problems []string, generation int64) {
	if len(problems) == 0 {
		meta.RemoveStatusCondition(conds, ConditionExceptionsApplied)
		return
	}
	meta.SetStatusCondition(conds, metav1.Condition{
		Type: ConditionExceptionsApplied, Status: metav1.ConditionFalse, Reason: "Invalid", ObservedGeneration: generation,
		Message: "Not applied: " + strings.Join(problems, "; ") + ".",
	})
}

// calendars returns the shared holiday calendars of the CostDeckConfig. Without one,
// schedules naming a calendar report it as missing.
func calendars(ctx context.Context, c client.Reader) []finopsv1.HolidayCalendar {
	cfg, err := config.Get(ctx, c)
	if err != nil {
		logf.FromContext(ctx).Error(err, "Could not read the holiday calendars")
		return nil
	}
	return cfg.Spec.Calendars
}

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
const recentWakeUps = 5

func trackWakeUp(st *finopsv1.ScheduleStatus, oldPhase, newPhase string, now time.Time) {
	switch {
	case oldPhase == scaling.PhaseScaledDown && (newPhase == scaling.PhaseScalingUp || newPhase == scaling.PhaseWaitingForDependencies):
		started := metav1.NewTime(now)
		st.WakeUpStartedAt = &started
	case newPhase == scaling.PhaseScaledUp && st.WakeUpStartedAt != nil:
		took := int32(now.Sub(st.WakeUpStartedAt.Time).Round(time.Second) / time.Second)
		st.RecentWakeUps = append([]finopsv1.WakeUp{{StartedAt: *st.WakeUpStartedAt, Seconds: max(took, 0)}}, st.RecentWakeUps...)
		st.RecentWakeUps = st.RecentWakeUps[:min(len(st.RecentWakeUps), recentWakeUps)]
		st.WakeUpStartedAt = nil
	case newPhase == scaling.PhaseScalingDown || newPhase == scaling.PhaseScaledDown:
		st.WakeUpStartedAt = nil
	}
}

func applyDecision(st *finopsv1.ScheduleStatus, conds *[]metav1.Condition, d scaling.Decision, generation int64, kind string) {
	st.Mode = d.Mode
	st.DesiredState = d.State()
	st.PrewarmMinutes = int32(d.PrewarmLead / time.Minute)
	st.Exception, st.ExceptionEndsAt = d.Exception, nil
	if d.ExceptionEndsAt != nil {
		t := metav1.NewTime(*d.ExceptionEndsAt)
		st.ExceptionEndsAt = &t
	}
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
	case scaling.ModeIdle:
		cond.Status, cond.Reason = metav1.ConditionFalse, "Idle"
		cond.Message = fmt.Sprintf("The %s sleeps for being unused (status.idleUntil); a manual start wakes it.", kind)
	case scaling.ModePrewarm:
		cond.Status, cond.Reason = metav1.ConditionFalse, "Prewarm"
		cond.Message = fmt.Sprintf("The %s is started %d minutes ahead of its schedule so it is ready when the schedule starts (spec.prewarm).", kind, int(d.PrewarmLead/time.Minute))
	case scaling.ModeException:
		cond.Status, cond.Reason = metav1.ConditionFalse, "Exception"
		cond.Message = fmt.Sprintf("The %s is kept %s for %q until %s; then the schedule takes over.", kind, strings.ToLower(d.State()), d.Exception, d.ExceptionEndsAt.UTC().Format(time.RFC3339))
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
	case scaling.ModeIdle:
		why = "because nobody used it"
	case scaling.ModePrewarm:
		why = fmt.Sprintf("%d minutes ahead of its schedule, to be ready on time", st.PrewarmMinutes)
	case scaling.ModeException:
		why = "for " + st.Exception
		if st.ExceptionEndsAt != nil {
			why += " until " + st.ExceptionEndsAt.UTC().Format("Mon 2 Jan 15:04 MST")
		}
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
