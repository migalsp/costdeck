package api

import (
	"context"
	"fmt"
	"net/http"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
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
	writeJSON(w, http.StatusOK, list.Items)
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
	Name   string                   `json:"name"`
	Status string                   `json:"status"`
	CPU    finopsv1.ResourceMetrics `json:"cpu"`
	Memory finopsv1.ResourceMetrics `json:"memory"`
	Cost   *CostResponse            `json:"cost,omitempty"`
}

func (s *Server) servePods(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nsName := r.PathValue("ns")

	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	provider := "local"
	if cfg.Spec.Providers.AWS != nil && cfg.Spec.Providers.AWS.Enabled {
		provider = "aws"
	}
	cpuRate, ramRate := getDefaultRates(provider)

	podMetricsMapCPU := make(map[string]string)
	podMetricsMapMem := make(map[string]string)

	if s.MetricsClient != nil {
		pmList, err := s.MetricsClient.MetricsV1beta1().PodMetricses(nsName).List(ctx, metav1.ListOptions{})
		if err == nil {
			for _, pm := range pmList.Items {
				var cpuUsage, memUsage resource.Quantity
				for _, c := range pm.Containers {
					cpuUsage.Add(*c.Usage.Cpu())
					memUsage.Add(*c.Usage.Memory())
				}
				podMetricsMapCPU[pm.Name] = cpuUsage.String()
				podMetricsMapMem[pm.Name] = memUsage.String()
			}
		}
	}

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

		cpuU := podMetricsMapCPU[p.Name]
		memU := podMetricsMapMem[p.Name]
		if cpuU == "" {
			cpuU = "0"
		}
		if memU == "" {
			memU = "0"
		}

		var podCost *CostResponse
		if p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed {
			cpuCores := float64(cpuReq.MilliValue()) / 1000.0
			ramGb := float64(memReq.Value()) / 1024.0 / 1024.0 / 1024.0
			hourly := (cpuCores * cpuRate) + (ramGb * ramRate)

			determinedBy := "Heuristic Math Pricing"
			if provider != "local" {
				determinedBy = fmt.Sprintf("%s (%s)", determinedBy, provider)
			}

			podCost = &CostResponse{
				HourlyCost:   hourly,
				MonthlyCost:  hourly * 730,
				Currency:     "USD",
				DeterminedBy: determinedBy,
			}
		}

		details = append(details, PodDetail{
			Name:   p.Name,
			Status: string(p.Status.Phase),
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

func (s *Server) handleNamespaceOptimize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nsName := r.PathValue("ns")

	// 1. Calculate Average Usage from NamespaceFinOps (last 60 mins)
	avgCpuNs, avgMemNs, err := s.calculateAverageUsage(ctx, nsName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 2. Get current individual usage from Metrics API
	currentCpuNs, currentMemNs, workloadUsage, workloadMemUsage, err := s.getCurrentUsage(ctx, nsName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 3. Compute Correction Factor
	cpuFactor := computeFactor(avgCpuNs, currentCpuNs)
	memFactor := computeFactor(avgMemNs, currentMemNs)

	// 4. Update Workloads and Store Optimization Info
	optimizedWorkloads, err := s.optimizeWorkloads(ctx, nsName, cpuFactor, memFactor, workloadUsage, workloadMemUsage)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 5. Store/Update NamespaceOptimization CR
	if err := s.updateOptimizationCR(ctx, nsName, optimizedWorkloads); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) calculateAverageUsage(ctx context.Context, nsName string) (float64, float64, error) {
	finOps, err := s.findNamespaceFinOps(ctx, nsName)
	if err != nil {
		return 0, 0, fmt.Errorf("NamespaceFinOps not found: %w", err)
	}

	if len(finOps.Status.History) == 0 {
		return 0, 0, fmt.Errorf("no history available for optimization")
	}

	var totalCpuAv, totalMemAv float64
	for _, dp := range finOps.Status.History {
		cpuQ, _ := resource.ParseQuantity(dp.CPU.Usage)
		memQ, _ := resource.ParseQuantity(dp.Memory.Usage)
		totalCpuAv += cpuQ.AsApproximateFloat64()
		totalMemAv += float64(memQ.Value())
	}
	avgCpuNs := totalCpuAv / float64(len(finOps.Status.History))
	avgMemNs := totalMemAv / float64(len(finOps.Status.History))
	return avgCpuNs, avgMemNs, nil
}

func (s *Server) getCurrentUsage(ctx context.Context, nsName string) (float64, float64, map[string]float64, map[string]float64, error) {
	if s.MetricsClient == nil {
		return 0, 0, nil, nil, fmt.Errorf("metrics API is not available")
	}
	podMetricsList, err := s.MetricsClient.MetricsV1beta1().PodMetricses(nsName).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, nil, nil, fmt.Errorf("failed to get metrics: %w", err)
	}

	var currentCpuNs, currentMemNs float64
	workloadUsage := make(map[string]float64)
	workloadMemUsage := make(map[string]float64)

	for _, pm := range podMetricsList.Items {
		workloadName, workloadKind := s.getWorkloadOwner(ctx, nsName, pm.OwnerReferences)
		if workloadName == "" {
			continue
		}

		key := workloadKind + "/" + workloadName
		for _, c := range pm.Containers {
			cpu := c.Usage.Cpu().AsApproximateFloat64()
			mem := float64(c.Usage.Memory().Value())
			currentCpuNs += cpu
			currentMemNs += mem
			workloadUsage[key] += cpu
			workloadMemUsage[key] += mem
		}
	}
	return currentCpuNs, currentMemNs, workloadUsage, workloadMemUsage, nil
}

func (s *Server) getWorkloadOwner(ctx context.Context, nsName string, ownerReferences []metav1.OwnerReference) (string, string) {
	for _, or := range ownerReferences {
		switch or.Kind {
		case "ReplicaSet":
			var rs appsv1.ReplicaSet
			if err := s.Client.Get(ctx, client.ObjectKey{Name: or.Name, Namespace: nsName}, &rs); err == nil {
				for _, rsor := range rs.OwnerReferences {
					if rsor.Kind == kindDeployment {
						return rsor.Name, kindDeployment
					}
				}
			}
		case kindStatefulSet:
			return or.Name, kindStatefulSet
		}
	}
	return "", ""
}

func computeFactor(avg, current float64) float64 {
	if current > 0 {
		return avg / current
	}
	return 1.0
}

func (s *Server) optimizeWorkloads(ctx context.Context, nsName string, cpuFactor, memFactor float64, workloadUsage, workloadMemUsage map[string]float64) ([]finopsv1.WorkloadOptimization, error) {
	var optimizedWorkloads []finopsv1.WorkloadOptimization

	deploys := &appsv1.DeploymentList{}
	if err := s.Client.List(ctx, deploys, client.InNamespace(nsName)); err != nil {
		return nil, err
	}
	for i := range deploys.Items {
		opt, ok, err := s.optimizeSingleWorkload(ctx, &deploys.Items[i], kindDeployment, cpuFactor, memFactor, workloadUsage, workloadMemUsage)
		if err != nil {
			return nil, err
		}
		if ok {
			optimizedWorkloads = append(optimizedWorkloads, opt)
		}
	}

	stss := &appsv1.StatefulSetList{}
	if err := s.Client.List(ctx, stss, client.InNamespace(nsName)); err != nil {
		return nil, err
	}
	for i := range stss.Items {
		opt, ok, err := s.optimizeSingleWorkload(ctx, &stss.Items[i], kindStatefulSet, cpuFactor, memFactor, workloadUsage, workloadMemUsage)
		if err != nil {
			return nil, err
		}
		if ok {
			optimizedWorkloads = append(optimizedWorkloads, opt)
		}
	}

	return optimizedWorkloads, nil
}

func (s *Server) optimizeSingleWorkload(ctx context.Context, obj client.Object, kind string, cpuFactor, memFactor float64, workloadUsage, workloadMemUsage map[string]float64) (finopsv1.WorkloadOptimization, bool, error) {
	name := obj.GetName()
	key := kind + "/" + name

	var replicas int32
	var podSpec *corev1.PodTemplateSpec

	switch o := obj.(type) {
	case *appsv1.Deployment:
		if o.Spec.Replicas != nil {
			replicas = *o.Spec.Replicas
		}
		podSpec = &o.Spec.Template
	case *appsv1.StatefulSet:
		if o.Spec.Replicas != nil {
			replicas = *o.Spec.Replicas
		}
		podSpec = &o.Spec.Template
	}

	if replicas == 0 || podSpec == nil || len(podSpec.Spec.Containers) == 0 {
		return finopsv1.WorkloadOptimization{}, false, nil
	}

	usageCPU := workloadUsage[key] * cpuFactor
	usageMem := workloadMemUsage[key] * memFactor

	newReqCPU := usageCPU * 1.3 / float64(replicas)
	newLimCPU := usageCPU * 1.5 / float64(replicas)
	newReqMem := usageMem * 1.3 / float64(replicas)
	newLimMem := usageMem * 1.5 / float64(replicas)

	container := &podSpec.Spec.Containers[0]
	currentReqCPU := container.Resources.Requests.Cpu().AsApproximateFloat64()
	currentReqMem := float64(container.Resources.Requests.Memory().Value())
	currentLimCPU := container.Resources.Limits.Cpu().AsApproximateFloat64()
	currentLimMem := float64(container.Resources.Limits.Memory().Value())

	cpuFloor := 0.02
	memFloor := 64.0 * 1024 * 1024

	newReqCPU = applyFloor(newReqCPU, currentReqCPU, cpuFloor)
	newLimCPU = applyFloor(newLimCPU, currentLimCPU, cpuFloor*1.5)
	newReqMem = applyFloor(newReqMem, currentReqMem, memFloor)
	newLimMem = applyFloor(newLimMem, currentLimMem, memFloor*1.5)

	if newLimCPU < newReqCPU {
		newLimCPU = newReqCPU
	}
	if newLimMem < newReqMem {
		newLimMem = newReqMem
	}

	orig := finopsv1.ResourceValues{
		CPURequest:    container.Resources.Requests.Cpu().String(),
		CPULimit:      container.Resources.Limits.Cpu().String(),
		MemoryRequest: container.Resources.Requests.Memory().String(),
		MemoryLimit:   container.Resources.Limits.Memory().String(),
	}

	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	container.Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(int64(newReqCPU*1000), resource.DecimalSI),
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", int64(newReqMem/1024/1024))),
	}
	container.Resources.Limits = corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(int64(newLimCPU*1000), resource.DecimalSI),
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", int64(newLimMem/1024/1024))),
	}

	if err := s.Client.Patch(ctx, obj, patch); err != nil {
		return finopsv1.WorkloadOptimization{}, false, fmt.Errorf("could not right-size %s %s: %w", kind, name, err)
	}

	return finopsv1.WorkloadOptimization{
		Name:     name,
		Kind:     kind,
		Original: orig,
		Optimized: finopsv1.ResourceValues{
			CPURequest:    container.Resources.Requests.Cpu().String(),
			CPULimit:      container.Resources.Limits.Cpu().String(),
			MemoryRequest: container.Resources.Requests.Memory().String(),
			MemoryLimit:   container.Resources.Limits.Memory().String(),
		},
	}, true, nil
}

func applyFloor(newValue, current, floor float64) float64 {
	if newValue < floor {
		if current >= floor {
			return floor
		}
		return current
	}
	return newValue
}

func (s *Server) updateOptimizationCR(ctx context.Context, nsName string, workloads []finopsv1.WorkloadOptimization) error {
	operatorNs := config.OperatorNamespace()
	opt := &finopsv1.NamespaceOptimization{}
	err := s.Client.Get(ctx, client.ObjectKey{Name: nsName, Namespace: operatorNs}, opt)
	switch {
	case apierrors.IsNotFound(err):
		opt = &finopsv1.NamespaceOptimization{
			ObjectMeta: metav1.ObjectMeta{Name: nsName, Namespace: operatorNs},
			Spec:       finopsv1.NamespaceOptimizationSpec{TargetNamespace: nsName},
		}
		if err := s.Client.Create(ctx, opt); err != nil {
			return fmt.Errorf("failed to create NamespaceOptimization: %w", err)
		}
	case err != nil:
		return fmt.Errorf("failed to read NamespaceOptimization: %w", err)
	}

	opt.Status.Active = true
	opt.Status.OptimizedAt = metav1.Now()
	opt.Status.Workloads = workloads

	if err := s.Client.Status().Update(ctx, opt); err != nil {
		return fmt.Errorf("failed to update NamespaceOptimization status: %w", err)
	}
	return nil
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
	ctx := r.Context()
	nsName := r.PathValue("ns")

	var opt finopsv1.NamespaceOptimization
	if err := s.Client.Get(ctx, client.ObjectKey{Name: nsName, Namespace: config.OperatorNamespace()}, &opt); err != nil {
		writeError(w, http.StatusNotFound, "Optimization info not found")
		return
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
			writeErrorf(w, http.StatusInternalServerError, "could not restore %s %s: %v", wl.Kind, wl.Name, err)
			return
		}
	}

	opt.Status.Active = false
	if err := s.Client.Status().Update(ctx, &opt); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
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
