package api

import (
	"context"
	"fmt"
	"net/http"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// Workload kinds CostDeck scales and right-sizes.
const (
	kindDeployment  = "Deployment"
	kindStatefulSet = "StatefulSet"
)

func (s *Server) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	var list finopsv1.NamespaceFinOpsList
	if err := s.Client.List(r.Context(), &list, client.InNamespace(config.OperatorNamespace())); err != nil {
		logf.Log.Error(err, "Failed to list NamespaceFinOps")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeObjects(w, http.StatusOK, list.Items)
}

// findNamespaceFinOps resolves the NamespaceFinOps tracking a namespace. Auto-discovered
// objects are named after their target, but hand-made ones may not be.
func (s *Server) findNamespaceFinOps(ctx context.Context, nsName string) (*finopsv1.NamespaceFinOps, error) {
	nsFinOps := &finopsv1.NamespaceFinOps{}
	err := s.Client.Get(ctx, client.ObjectKey{Name: nsName, Namespace: config.OperatorNamespace()}, nsFinOps)
	if err == nil {
		return nsFinOps, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	var list finopsv1.NamespaceFinOpsList
	if err := s.Client.List(ctx, &list, client.InNamespace(config.OperatorNamespace())); err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].Spec.TargetNamespace == nsName {
			return &list.Items[i], nil
		}
	}
	return nil, apierrors.NewNotFound(finopsv1.GroupVersion.WithResource("namespacefinops").GroupResource(), nsName)
}

func (s *Server) serveHistory(w http.ResponseWriter, r *http.Request) {
	nsFinOps, err := s.findNamespaceFinOps(r.Context(), r.PathValue("ns"))
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "Not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	history := nsFinOps.Status.History
	if history == nil {
		history = []finopsv1.MetricDataPoint{}
	}
	writeJSON(w, http.StatusOK, history)
}

// PodDetail is one pod row of the namespace details view.
type PodDetail struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Ready is true when every container passes its readiness check.
	Ready bool `json:"ready"`
	// Reason explains a pod that is not healthy although its phase may say Running, such
	// as CrashLoopBackOff, ImagePullBackOff or OOMKilled.
	Reason   string                   `json:"reason,omitempty"`
	Restarts int32                    `json:"restarts"`
	CPU      finopsv1.ResourceMetrics `json:"cpu"`
	Memory   finopsv1.ResourceMetrics `json:"memory"`
	Cost     *CostResponse            `json:"cost,omitempty"`
}

// podHealth returns readiness, the most telling problem reason and the restart count.
func podHealth(p *corev1.Pod) (ready bool, reason string, restarts int32) {
	ready = p.Status.Phase == corev1.PodRunning
	for _, c := range p.Status.InitContainerStatuses {
		if w := c.State.Waiting; w != nil && w.Reason != "" && w.Reason != "PodInitializing" {
			reason = "Init:" + w.Reason
		}
	}
	for _, c := range p.Status.ContainerStatuses {
		restarts += c.RestartCount
		ready = ready && c.Ready
		switch {
		case c.State.Waiting != nil && c.State.Waiting.Reason != "" && c.State.Waiting.Reason != "ContainerCreating":
			reason = c.State.Waiting.Reason
		case c.State.Terminated != nil && c.State.Terminated.Reason != "" && reason == "":
			reason = c.State.Terminated.Reason
		case reason == "" && c.LastTerminationState.Terminated != nil && c.LastTerminationState.Terminated.Reason == "OOMKilled":
			reason = "OOMKilled"
		}
	}
	if reason == "" && p.Status.Phase == corev1.PodPending {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason != "" {
				reason = c.Reason // Unschedulable
			}
		}
	}
	return ready, reason, restarts
}

func (s *Server) servePods(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nsName := r.PathValue("ns")

	rates := s.costRates(ctx)

	podUsage, source, err := s.metricsProvider().PodUsage(ctx, nsName)
	if err != nil {
		// Requests, limits and cost are still useful without live usage.
		logf.Log.Error(err, "Could not read pod usage", "namespace", nsName)
	}
	w.Header().Set("X-Metrics-Source", source.Source)

	var podList corev1.PodList
	if err := s.Client.List(ctx, &podList, client.InNamespace(nsName)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	details := []PodDetail{}
	for _, p := range podList.Items {
		var cpuReq, memReq, cpuLim, memLim resource.Quantity
		for _, c := range p.Spec.Containers {
			cpuReq.Add(*c.Resources.Requests.Cpu())
			memReq.Add(*c.Resources.Requests.Memory())
			cpuLim.Add(*c.Resources.Limits.Cpu())
			memLim.Add(*c.Resources.Limits.Memory())
		}

		u := podUsage[p.Name]
		cpuU, memU := u.CPU.String(), u.Memory.String()

		var podCost *CostResponse
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			hourly := rates.Hourly(cpuReq, memReq)
			podCost = &CostResponse{
				HourlyCost:   hourly,
				MonthlyCost:  hourly * pricing.HoursPerMonth,
				Currency:     rates.Currency,
				DeterminedBy: rates.Basis,
			}
		}

		ready, reason, restarts := podHealth(&p)
		details = append(details, PodDetail{
			Name:     p.Name,
			Status:   string(p.Status.Phase),
			Ready:    ready,
			Reason:   reason,
			Restarts: restarts,
			CPU: finopsv1.ResourceMetrics{
				Usage:    cpuU,
				Requests: cpuReq.String(),
				Limits:   cpuLim.String(),
			},
			Memory: finopsv1.ResourceMetrics{
				Usage:    memU,
				Requests: memReq.String(),
				Limits:   memLim.String(),
			},
			Cost: podCost,
		})
	}

	writeJSON(w, http.StatusOK, details)
}

// WorkloadDetail is one Deployment or StatefulSet row of the namespace workloads view.
type WorkloadDetail struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Replicas      int32  `json:"replicas"`
	ReadyReplicas int32  `json:"readyReplicas"`
	Status        string `json:"status"` // running, scaled-down
}

func newWorkloadDetail(name, kind string, specReplicas *int32, ready int32) WorkloadDetail {
	replicas := int32(1)
	if specReplicas != nil {
		replicas = *specReplicas
	}
	status := "running"
	if replicas == 0 {
		status = "scaled-down"
	}
	return WorkloadDetail{Name: name, Kind: kind, Replicas: replicas, ReadyReplicas: ready, Status: status}
}

func (s *Server) serveWorkloads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nsName := r.PathValue("ns")
	result := []WorkloadDetail{}

	deployments := &appsv1.DeploymentList{}
	if err := s.Client.List(ctx, deployments, client.InNamespace(nsName)); err == nil {
		for _, d := range deployments.Items {
			result = append(result, newWorkloadDetail(d.Name, kindDeployment, d.Spec.Replicas, d.Status.ReadyReplicas))
		}
	}

	statefulSets := &appsv1.StatefulSetList{}
	if err := s.Client.List(ctx, statefulSets, client.InNamespace(nsName)); err == nil {
		for _, sts := range statefulSets.Items {
			result = append(result, newWorkloadDetail(sts.Name, kindStatefulSet, sts.Spec.Replicas, sts.Status.ReadyReplicas))
		}
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) serveWorkloadAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := client.ObjectKey{Name: r.PathValue("name"), Namespace: r.PathValue("ns")}

	var req struct {
		Kind     string `json:"kind"`
		Replicas int32  `json:"replicas"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Replicas < 0 {
		writeError(w, http.StatusBadRequest, "replicas must not be negative")
		return
	}

	var obj client.Object
	switch req.Kind {
	case kindDeployment:
		obj = &appsv1.Deployment{}
	case kindStatefulSet:
		obj = &appsv1.StatefulSet{}
	default:
		writeError(w, http.StatusBadRequest, "Unknown kind")
		return
	}

	if err := s.Client.Get(ctx, key, obj); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	switch o := obj.(type) {
	case *appsv1.Deployment:
		o.Spec.Replicas = &req.Replicas
	case *appsv1.StatefulSet:
		o.Spec.Replicas = &req.Replicas
	}
	if err := s.Client.Patch(ctx, obj, patch); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

// metricsProvider returns the configured metrics provider, or a metrics-server-only one
// when the server was built without it (tests).
func (s *Server) metricsProvider() *metrics.Provider {
	if s.Metrics != nil {
		return s.Metrics
	}
	return metrics.NewProvider(s.Client, &metrics.MetricsServerSource{Client: s.MetricsClient})
}

// originalResources rebuilds a ResourceList from stored values, skipping the ones that
// were never set. resource.MustParse would panic on an empty string.
func originalResources(cpu, mem string) corev1.ResourceList {
	list := corev1.ResourceList{}
	if q, err := resource.ParseQuantity(cpu); err == nil && cpu != "" && !q.IsZero() {
		list[corev1.ResourceCPU] = q
	}
	if q, err := resource.ParseQuantity(mem); err == nil && mem != "" && !q.IsZero() {
		list[corev1.ResourceMemory] = q
	}
	return list
}

func (s *Server) handleNamespaceRevert(w http.ResponseWriter, r *http.Request) {
	if err := s.revertNamespace(r.Context(), r.PathValue("ns")); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "Optimization info not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

// revertNamespace restores the requests and limits recorded before right-sizing.
func (s *Server) revertNamespace(ctx context.Context, nsName string) error {
	var opt finopsv1.NamespaceOptimization
	if err := s.Client.Get(ctx, client.ObjectKey{Name: nsName, Namespace: config.OperatorNamespace()}, &opt); err != nil {
		return err
	}

	for _, wl := range opt.Status.Workloads {
		var obj client.Object
		switch wl.Kind {
		case kindDeployment:
			obj = &appsv1.Deployment{}
		case kindStatefulSet:
			obj = &appsv1.StatefulSet{}
		default:
			continue
		}
		if err := s.Client.Get(ctx, client.ObjectKey{Name: wl.Name, Namespace: nsName}, obj); err != nil {
			continue // The workload is gone; nothing to restore.
		}
		patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
		var containers []corev1.Container
		switch o := obj.(type) {
		case *appsv1.Deployment:
			containers = o.Spec.Template.Spec.Containers
		case *appsv1.StatefulSet:
			containers = o.Spec.Template.Spec.Containers
		}
		if len(containers) == 0 {
			continue
		}
		containers[0].Resources.Requests = originalResources(wl.Original.CPURequest, wl.Original.MemoryRequest)
		containers[0].Resources.Limits = originalResources(wl.Original.CPULimit, wl.Original.MemoryLimit)
		if err := s.Client.Patch(ctx, obj, patch); err != nil {
			return fmt.Errorf("could not restore %s %s: %w", wl.Kind, wl.Name, err)
		}
	}

	opt.Status.Active = false
	return s.Client.Status().Update(ctx, &opt)
}

func (s *Server) handleNamespaceOptimizationInfo(w http.ResponseWriter, r *http.Request) {
	var opt finopsv1.NamespaceOptimization
	key := client.ObjectKey{Name: r.PathValue("ns"), Namespace: config.OperatorNamespace()}
	if err := s.Client.Get(r.Context(), key, &opt); err != nil {
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusOK, map[string]any{"active": false})
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, opt.Status)
}
