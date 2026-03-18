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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// NamespaceFinOpsReconciler reconciles a NamespaceFinOps object
type NamespaceFinOpsReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	MetricsClient metricsv.Interface
}

// +kubebuilder:rbac:groups=finops.costdeck.io,resources=namespacefinops,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=finops.costdeck.io,resources=namespacefinops/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=finops.costdeck.io,resources=namespacefinops/finalizers,verbs=update

// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=metrics.k8s.io,resources=pods,verbs=get;list;watch
func (r *NamespaceFinOpsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var nsFinOps finopsv1.NamespaceFinOps
	if err := r.Get(ctx, req.NamespacedName, &nsFinOps); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	targetNs := nsFinOps.Spec.TargetNamespace

	// 1. Get current usage from metrics API
	podMetricsList, err := r.MetricsClient.MetricsV1beta1().PodMetricses(targetNs).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Error(err, "unable to fetch pod metrics", "namespace", targetNs)
		return ctrl.Result{RequeueAfter: time.Minute}, nil // Soft fail
	}

	var totalCpuUsage resource.Quantity
	var totalMemUsage resource.Quantity
	for _, pm := range podMetricsList.Items {
		for _, c := range pm.Containers {
			totalCpuUsage.Add(*c.Usage.Cpu())
			totalMemUsage.Add(*c.Usage.Memory())
		}
	}

	// 2. Get current limits and requests from regular pods
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(targetNs)); err != nil {
		log.Error(err, "unable to list pods", "namespace", targetNs)
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
		log.Error(err, "unable to update status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceFinOpsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&finopsv1.NamespaceFinOps{}).
		Named("namespacefinops").
		Complete(r)
}
