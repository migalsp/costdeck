package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// ConditionIdle says whether idle auto-sleep watches a schedule's namespaces and whether
// it keeps them down.
const ConditionIdle = "Idle"

// idleSleepLongest caps an idle sleep when the schedule does not change by itself sooner.
const idleSleepLongest = 7 * 24 * time.Hour

// RequestCounter counts the requests that reached a namespace; metrics.Provider does.
type RequestCounter interface {
	NamespaceRequests(ctx context.Context, ns string, window time.Duration) (float64, bool, error)
}

// GPUMeter reports the busiest a namespace's GPUs were, in percent; metrics.Provider does.
// A RequestCounter that is also a GPUMeter lets GPUs keep namespaces awake.
type GPUMeter interface {
	NamespaceGPUPeak(ctx context.Context, ns string, window time.Duration) (float64, bool, error)
}

// idleCheck is what idle auto-sleep needs to know about a schedule.
type idleCheck struct {
	UID        types.UID
	Generation int64
	IdleAfter  *metav1.Duration
	Namespaces []string
	Phase      string
	// UpSince is when the namespaces were last brought up.
	UpSince time.Time
	// Required is set while dependent groups need the namespaces up.
	Required bool
	// At is what the timetable decides at a given time.
	At func(time.Time) scaling.Decision
}

// idleWatch measures the traffic of the schedules with spec.idleAfter, at most once a
// minute each, so that watch events do not flood VictoriaMetrics.
type idleWatch struct {
	mu       sync.Mutex
	measured map[types.UID]time.Time
}

func (w *idleWatch) due(uid types.UID, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if last, ok := w.measured[uid]; ok && now.Sub(last) < time.Minute {
		return false
	}
	if w.measured == nil {
		w.measured = map[types.UID]time.Time{}
	}
	w.measured[uid] = now
	return true
}

// reconcile puts a schedule's namespaces to sleep once they have been up for the idle
// period without a request, and returns the decision to act on. Anything else that takes
// charge (a manual start, an exception, dependents, the end of the sleep) ends the sleep.
func (w *idleWatch) reconcile(ctx context.Context, counter RequestCounter, st *finopsv1.ScheduleStatus, conds *[]metav1.Condition, c idleCheck, d scaling.Decision, now time.Time) scaling.Decision {
	set := func(status metav1.ConditionStatus, reason, msg string) {
		meta.SetStatusCondition(conds, metav1.Condition{Type: ConditionIdle, Status: status, Reason: reason, Message: msg, ObservedGeneration: c.Generation})
	}
	var period time.Duration
	if c.IdleAfter != nil {
		period = c.IdleAfter.Duration
	}
	if period <= 0 {
		st.IdleUntil = nil
		meta.RemoveStatusCondition(conds, ConditionIdle)
		return d
	}
	if d.Mode == scaling.ModeIdle {
		return d
	}
	watching := fmt.Sprintf("The namespaces sleep once nobody uses them for %s within the schedule's hours.", period)
	if st.IdleUntil != nil || meta.FindStatusCondition(*conds, ConditionIdle) == nil {
		st.IdleUntil = nil
		set(metav1.ConditionFalse, "Watching", watching)
	}
	if d.Mode != scaling.ModeSchedule || !d.Active || c.Required || c.Phase != scaling.PhaseScaledUp ||
		now.Sub(c.UpSince) < period || counter == nil || !w.due(c.UID, now) {
		return d
	}

	u, ns, err := measure(ctx, counter, c.Namespaces, period)
	switch {
	case errors.Is(err, metrics.ErrNoHistory):
		set(metav1.ConditionFalse, "NoVictoriaMetrics", "Idle auto-sleep counts requests and GPU use in VictoriaMetrics, which is not enabled.")
		return d
	case err != nil:
		logf.FromContext(ctx).Error(err, "Could not measure the use of a namespace", "namespace", ns)
		set(metav1.ConditionFalse, "Failed", "Could not measure the use: "+err.Error())
		return d
	case !u.known():
		set(metav1.ConditionFalse, "NoTrafficData", "No Envoy Gateway, Istio or ingress-nginx requests and no DCGM GPU metrics for these namespaces in VictoriaMetrics, so they are never treated as unused.")
		return d
	case !scaling.IdleFor(period, c.UpSince, now, u.used(), u.known()):
		why := "Requests reached the namespaces"
		if u.requests == 0 {
			why = "Their GPUs were busy"
		}
		set(metav1.ConditionFalse, "InUse", fmt.Sprintf("%s in the last %s.", why, period))
		return d
	}
	until := now.Add(idleSleepLongest)
	if d.NextTransition != nil && d.NextTransition.Before(until) {
		until = *d.NextTransition
	}
	t := metav1.NewTime(until)
	st.IdleUntil = &t
	d = scaling.ApplyIdle(d, &until, now, c.At)
	back := "until someone starts them"
	if d.NextTransition != nil {
		back = "until " + d.NextTransition.UTC().Format(time.RFC3339) + " or until someone starts them"
	}
	set(metav1.ConditionTrue, "Unused", fmt.Sprintf("%s for %s, so they sleep %s.", u.unused(c.Namespaces), period, back))
	logf.FromContext(ctx).Info("Put namespaces to sleep for being unused", "namespaces", c.Namespaces, "idleAfter", period, "until", until)
	return d
}

// usage is what the idle check measured over the period.
type usage struct {
	requests, gpuPeak       float64
	requestsKnown, gpuKnown bool
}

func (u usage) known() bool   { return u.requestsKnown || u.gpuKnown }
func (u usage) gpuBusy() bool { return u.gpuKnown && u.gpuPeak >= scaling.GPUIdlePercent }
func (u usage) used() bool    { return u.requests > 0 || u.gpuBusy() }

// unused says what was not used, for the condition.
func (u usage) unused(namespaces []string) string {
	names := strings.Join(namespaces, ", ")
	switch {
	case u.gpuKnown && u.requestsKnown:
		return "No requests reached " + names + " and their GPUs stayed idle"
	case u.gpuKnown:
		return "The GPUs of " + names + " stayed idle"
	}
	return "No requests reached " + names
}

// measure adds up the requests to the namespaces and finds their busiest GPU; on an error
// it names the namespace it was measuring.
func measure(ctx context.Context, counter RequestCounter, namespaces []string, period time.Duration) (usage, string, error) {
	var u usage
	gpus, _ := counter.(GPUMeter)
	for _, ns := range namespaces {
		n, known, err := counter.NamespaceRequests(ctx, ns, period)
		if err != nil {
			return u, ns, err
		}
		u.requests += n
		u.requestsKnown = u.requestsKnown || known
		if gpus == nil {
			continue
		}
		peak, known, err := gpus.NamespaceGPUPeak(ctx, ns, period)
		if err != nil {
			return u, ns, err
		}
		u.gpuPeak = max(u.gpuPeak, peak)
		u.gpuKnown = u.gpuKnown || known
	}
	return u, "", nil
}
