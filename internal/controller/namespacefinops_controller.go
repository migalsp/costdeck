/*
Copyright 2026 migalsp.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/telemetry"
)

// NamespaceFinOpsReconciler reconciles a NamespaceFinOps object
type NamespaceFinOpsReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Metrics *metrics.Provider
	// Pricing values the namespace's requests for the cost metric; optional.
	Pricing *pricing.Resolver
	// FlushEvery is how often sampled points are written to the status; zero means
	// five minutes.
	FlushEvery time.Duration

	mu sync.Mutex
	// pending holds the points sampled since the last status write, by object name.
	pending map[string][]finopsv1.MetricDataPoint
	// sampledAt is when each object was last sampled.
	sampledAt map[string]time.Time
}

// Usage is sampled once a minute but written in batches: one status update per namespace
// per minute was most of the operator's write load on the API server. A new leader loses
// at most one unwritten batch.
const (
	sampleEvery       = time.Minute
	defaultFlushEvery = 5 * time.Minute
	historyPoints     = 60
)

// ConditionMetricsAvailable reports whether usage could be collected and from where.
const ConditionMetricsAvailable = "MetricsAvailable"

// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=namespacefinops,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=namespacefinops/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=namespacefinops/finalizers,verbs=update

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=metrics.k8s.io,resources=pods,verbs=get;list;watch

// Reconcile samples the tracked namespace's usage once a minute and writes the samples
// to the status every few minutes, or at once when its findings or the metrics condition
// change.
func (r *NamespaceFinOpsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var nsFinOps finopsv1.NamespaceFinOps
	if err := r.Get(ctx, req.NamespacedName, &nsFinOps); err != nil {
		if apierrors.IsNotFound(err) {
			telemetry.ForgetNamespace(req.Name)
			r.forget(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	now := time.Now()
	// A spec change can reconcile between samples; keep one sample a minute.
	if last := r.lastSample(req.Name, nsFinOps.Status.LastUpdated.Time); !last.IsZero() && now.Sub(last) < sampleEvery-5*time.Second {
		return ctrl.Result{RequeueAfter: sampleEvery - now.Sub(last)}, nil
	}

	targetNs := nsFinOps.Spec.TargetNamespace

	// 1. Current usage from the configured source. VictoriaMetrics is preferred when it is
	// enabled; metrics-server steps in when it fails, and the condition says so.
	usage, source, err := r.Metrics.NamespaceUsage(ctx, targetNs)
	if err != nil {
		log.Error(err, "Could not collect namespace usage", "namespace", targetNs, "degraded", source.Degraded)
		msg := err.Error()
		if source.Degraded != nil {
			msg = fmt.Sprintf("VictoriaMetrics: %v; metrics-server: %v", source.Degraded, err)
		}
		changed := meta.SetStatusCondition(&nsFinOps.Status.Conditions, metav1.Condition{
			Type: ConditionMetricsAvailable, Status: metav1.ConditionFalse,
			Reason: "Unavailable", Message: msg, ObservedGeneration: nsFinOps.Generation,
		})
		if changed {
			if uerr := r.Status().Update(ctx, &nsFinOps); uerr != nil {
				return ctrl.Result{}, uerr
			}
		}
		return ctrl.Result{RequeueAfter: sampleEvery}, nil
	}
	conditionChanged := meta.SetStatusCondition(&nsFinOps.Status.Conditions, metricsCondition(source, nsFinOps.Generation))
	totalCpuUsage, totalMemUsage := usage.CPU, usage.Memory

	// 2. Requests and limits of the running pods, from the cache.
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(targetNs)); err != nil {
		log.Error(err, "Could not list Pods", "namespace", targetNs)
		return ctrl.Result{RequeueAfter: sampleEvery}, nil
	}
	totals := sumPods(podList.Items)
	req2, lim := totals.requests, totals.limits
	insights := namespaceInsights(totals, totalCpuUsage, totalMemUsage, len(podList.Items) > 0)

	if r.Pricing != nil {
		rates := r.Pricing.Rates(ctx)
		telemetry.RecordNamespace(targetNs, totalCpuUsage.AsApproximateFloat64(), totalMemUsage.AsApproximateFloat64(),
			rates.Monthly(req2.cpu, req2.mem), rates.Currency)
	}

	// 3. Buffer the data point; write the buffer when it is due.
	points := r.addSample(req.Name, now, finopsv1.MetricDataPoint{
		Timestamp: metav1.NewTime(now),
		CPU:       finopsv1.ResourceMetrics{Usage: totalCpuUsage.String(), Requests: req2.cpu.String(), Limits: lim.cpu.String()},
		Memory:    finopsv1.ResourceMetrics{Usage: totalMemUsage.String(), Requests: req2.mem.String(), Limits: lim.mem.String()},
	})
	flushEvery := r.FlushEvery
	if flushEvery <= 0 {
		flushEvery = defaultFlushEvery
	}
	due := nsFinOps.Status.LastUpdated.IsZero() || now.Sub(nsFinOps.Status.LastUpdated.Time) >= flushEvery-5*time.Second
	if !due && !conditionChanged && slices.Equal(insights, nsFinOps.Status.Insights) {
		return ctrl.Result{RequeueAfter: sampleEvery}, nil
	}

	nsFinOps.Status.History = append(nsFinOps.Status.History, points...)
	if len(nsFinOps.Status.History) > historyPoints {
		nsFinOps.Status.History = nsFinOps.Status.History[len(nsFinOps.Status.History)-historyPoints:]
	}
	nsFinOps.Status.LastUpdated = metav1.NewTime(now)
	nsFinOps.Status.Insights = insights
	if err := r.Status().Update(ctx, &nsFinOps); err != nil {
		// The points stay buffered for the next attempt.
		log.Error(err, "Could not update NamespaceFinOps status")
		return ctrl.Result{}, err
	}
	r.flushed(req.Name, len(points))
	return ctrl.Result{RequeueAfter: sampleEvery}, nil
}

type resourcePair struct{ cpu, mem resource.Quantity }

// podTotals is what the running pods' containers request and are limited to, and
// whether any container lacks a request or a limit.
type podTotals struct {
	requests, limits               resourcePair
	missingRequests, missingLimits bool
}

// sumPods adds up the running pods' containers.
func sumPods(pods []corev1.Pod) podTotals {
	var t podTotals
	for _, p := range pods {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range p.Spec.Containers {
			cpuR, memR := c.Resources.Requests.Cpu(), c.Resources.Requests.Memory()
			cpuL, memL := c.Resources.Limits.Cpu(), c.Resources.Limits.Memory()
			t.requests.cpu.Add(*cpuR)
			t.requests.mem.Add(*memR)
			t.limits.cpu.Add(*cpuL)
			t.limits.mem.Add(*memL)
			t.missingRequests = t.missingRequests || cpuR.IsZero() || memR.IsZero()
			t.missingLimits = t.missingLimits || cpuL.IsZero() || memL.IsZero()
		}
	}
	return t
}

// namespaceInsights names what stands out about a namespace's requests and limits.
func namespaceInsights(t podTotals, cpuUsed, memUsed resource.Quantity, hasPods bool) []string {
	var insights []string
	if t.missingRequests {
		insights = append(insights, "Missing Requests")
	}
	if t.missingLimits {
		insights = append(insights, "Uncapped")
	}
	// Overprovisioned: usage below 30% of the requests.
	if !t.requests.cpu.IsZero() && cpuUsed.AsApproximateFloat64() < t.requests.cpu.AsApproximateFloat64()*0.3 {
		insights = append(insights, "Overprovisioned CPU")
	}
	if !t.requests.mem.IsZero() && memUsed.AsApproximateFloat64() < t.requests.mem.AsApproximateFloat64()*0.3 {
		insights = append(insights, "Overprovisioned RAM")
	}
	if len(insights) == 0 && hasPods {
		insights = append(insights, "Optimized")
	}
	return insights
}

// lastSample returns when an object was last sampled by this process, or its last
// status write after a restart.
func (r *NamespaceFinOpsReconciler) lastSample(name string, written time.Time) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.sampledAt[name]; ok {
		return t
	}
	return written
}

// addSample buffers a point and returns every point not yet written.
func (r *NamespaceFinOpsReconciler) addSample(name string, at time.Time, dp finopsv1.MetricDataPoint) []finopsv1.MetricDataPoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		r.pending, r.sampledAt = map[string][]finopsv1.MetricDataPoint{}, map[string]time.Time{}
	}
	buf := append(r.pending[name], dp)
	if len(buf) > historyPoints {
		buf = buf[len(buf)-historyPoints:]
	}
	r.pending[name], r.sampledAt[name] = buf, at
	return slices.Clone(buf)
}

// flushed drops the first n buffered points once they are written.
func (r *NamespaceFinOpsReconciler) flushed(name string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending[name] = r.pending[name][min(n, len(r.pending[name])):]
}

func (r *NamespaceFinOpsReconciler) forget(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, name)
	delete(r.sampledAt, name)
}

// metricsCondition describes which source answered the last usage query.
func metricsCondition(source metrics.Result, generation int64) metav1.Condition {
	cond := metav1.Condition{
		Type: ConditionMetricsAvailable, Status: metav1.ConditionTrue, ObservedGeneration: generation,
	}
	switch {
	case source.Source == metrics.SourceVictoriaMetrics:
		cond.Reason, cond.Message = "VictoriaMetrics", "Usage collected from VictoriaMetrics"
	case source.Degraded != nil:
		cond.Reason = "MetricsServerFallback"
		cond.Message = fmt.Sprintf("VictoriaMetrics is enabled but failed, using metrics-server: %v", source.Degraded)
	default:
		cond.Reason, cond.Message = "MetricsServer", "Usage collected from metrics-server"
	}
	return cond
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceFinOpsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Only spec changes trigger a reconcile; the periodic requeue drives sampling. Without
	// the predicate every status write would immediately enqueue another reconcile.
	return ctrl.NewControllerManagedBy(mgr).
		For(&finopsv1.NamespaceFinOps{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("namespacefinops").
		Complete(r)
}
