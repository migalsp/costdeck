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
}

// ConditionMetricsAvailable reports whether usage could be collected and from where.
const ConditionMetricsAvailable = "MetricsAvailable"

// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=namespacefinops,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=namespacefinops/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=finops.costdeck.io,namespace=costdeck,resources=namespacefinops/finalizers,verbs=update

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=metrics.k8s.io,resources=pods,verbs=get;list;watch

// Reconcile records one usage data point per minute for the tracked namespace.
func (r *NamespaceFinOpsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var nsFinOps finopsv1.NamespaceFinOps
	if err := r.Get(ctx, req.NamespacedName, &nsFinOps); err != nil {
		if apierrors.IsNotFound(err) {
			telemetry.ForgetNamespace(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
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
		meta.SetStatusCondition(&nsFinOps.Status.Conditions, metav1.Condition{
			Type: ConditionMetricsAvailable, Status: metav1.ConditionFalse,
			Reason: "Unavailable", Message: msg, ObservedGeneration: nsFinOps.Generation,
		})
		if uerr := r.Status().Update(ctx, &nsFinOps); uerr != nil {
			return ctrl.Result{}, uerr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	meta.SetStatusCondition(&nsFinOps.Status.Conditions, metricsCondition(source, nsFinOps.Generation))
	totalCpuUsage, totalMemUsage := usage.CPU, usage.Memory

	// 2. Get current limits and requests from regular pods
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(targetNs)); err != nil {
		log.Error(err, "Could not list Pods", "namespace", targetNs)
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	var totalCpuReq, totalMemReq resource.Quantity
	var totalCpuLim, totalMemLim resource.Quantity

	missingRequests := false
	missingLimits := false

	for _, p := range podList.Items {
		if p.Status.Phase != corev1.PodRunning {
			continue // Only count running pods
		}
		for _, c := range p.Spec.Containers {
			cpuR := c.Resources.Requests.Cpu()
			memR := c.Resources.Requests.Memory()
			cpuL := c.Resources.Limits.Cpu()
			memL := c.Resources.Limits.Memory()

			totalCpuReq.Add(*cpuR)
			totalMemReq.Add(*memR)
			totalCpuLim.Add(*cpuL)
			totalMemLim.Add(*memL)

			if cpuR.IsZero() || memR.IsZero() {
				missingRequests = true
			}
			if cpuL.IsZero() || memL.IsZero() {
				missingLimits = true
			}
		}
	}

	// 2.5 Calculate Insights
	var insights []string
	if missingRequests {
		insights = append(insights, "Missing Requests")
	}
	if missingLimits {
		insights = append(insights, "Uncapped")
	}

	// Overprovisioning check (Usage < 30% of Requests)
	if !totalCpuReq.IsZero() && totalCpuUsage.AsApproximateFloat64() < totalCpuReq.AsApproximateFloat64()*0.3 {
		insights = append(insights, "Overprovisioned CPU")
	}
	if !totalMemReq.IsZero() && totalMemUsage.AsApproximateFloat64() < totalMemReq.AsApproximateFloat64()*0.3 {
		insights = append(insights, "Overprovisioned RAM")
	}

	if len(insights) == 0 && len(podList.Items) > 0 {
		insights = append(insights, "Optimized")
	}

	if r.Pricing != nil {
		rates := r.Pricing.Rates(ctx)
		telemetry.RecordNamespace(targetNs, totalCpuUsage.AsApproximateFloat64(), totalMemUsage.AsApproximateFloat64(),
			rates.Monthly(totalCpuReq, totalMemReq), rates.Currency)
	}

	// 3. Create the data point
	now := metav1.Now()
	dp := finopsv1.MetricDataPoint{
		Timestamp: now,
		CPU: finopsv1.ResourceMetrics{
			Usage:    totalCpuUsage.String(),
			Requests: totalCpuReq.String(),
			Limits:   totalCpuLim.String(),
		},
		Memory: finopsv1.ResourceMetrics{
			Usage:    totalMemUsage.String(),
			Requests: totalMemReq.String(),
			Limits:   totalMemLim.String(),
		},
	}

	// 4. Update the history only if at least 1 minute has passed
	lastPointTime := nsFinOps.Status.LastUpdated.Time
	if !lastPointTime.IsZero() && time.Since(lastPointTime) < 55*time.Second {
		// Just update the insights and current state, but don't add a new history point yet
		nsFinOps.Status.Insights = insights
		if err := r.Status().Update(ctx, &nsFinOps); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	nsFinOps.Status.History = append(nsFinOps.Status.History, dp)
	if len(nsFinOps.Status.History) > 60 {
		nsFinOps.Status.History = nsFinOps.Status.History[len(nsFinOps.Status.History)-60:]
	}
	nsFinOps.Status.LastUpdated = now
	nsFinOps.Status.Insights = insights

	if err := r.Status().Update(ctx, &nsFinOps); err != nil {
		log.Error(err, "Could not update NamespaceFinOps status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: time.Minute}, nil
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
