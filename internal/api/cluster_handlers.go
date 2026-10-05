package api

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

func (s *Server) handleClusterInfo(w http.ResponseWriter, r *http.Request) {
	version, err := s.K8sClient.Discovery().ServerVersion()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"version":  version.GitVersion,
		"platform": version.Platform,
	})
}

func (s *Server) handleClusterNodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	k8sVer := s.getK8sVersion()

	nodes, err := s.K8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		writeErrorf(w, http.StatusInternalServerError, "Failed to list nodes: %v", err)
		return
	}

	nodeMetricsMap := s.getNodeMetricsMap(ctx)
	nodeReqCPU, nodeReqMem := s.getPodRequestsPerNode(ctx)

	var totalCapacityCPU, totalCapacityMem resource.Quantity
	var totalUsageCPU, totalUsageMem resource.Quantity
	var totalRequestedCPU, totalRequestedMem resource.Quantity
	nodeInfos := make([]map[string]any, 0, len(nodes.Items))

	for _, n := range nodes.Items {
		nodeInfos = append(nodeInfos, s.gatherNodeInfo(n, nodeMetricsMap, nodeReqCPU, nodeReqMem))

		capacity := n.Status.Allocatable
		totalCapacityCPU.Add(*capacity.Cpu())
		totalCapacityMem.Add(*capacity.Memory())

		if usage, ok := nodeMetricsMap[n.Name]; ok {
			totalUsageCPU.Add(*usage.Cpu())
			totalUsageMem.Add(*usage.Memory())
		}
		if q, ok := nodeReqCPU[n.Name]; ok {
			totalRequestedCPU.Add(*q)
		}
		if q, ok := nodeReqMem[n.Name]; ok {
			totalRequestedMem.Add(*q)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"k8sVersion": k8sVer,
		"totalCapacity": map[string]any{
			"cpu": totalCapacityCPU.AsApproximateFloat64(),
			"mem": totalCapacityMem.Value(),
		},
		"totalUsage": map[string]any{
			"cpu": totalUsageCPU.AsApproximateFloat64(),
			"mem": totalUsageMem.Value(),
		},
		"totalRequested": map[string]any{
			"cpu": totalRequestedCPU.AsApproximateFloat64(),
			"mem": totalRequestedMem.Value(),
		},
		"nodes": nodeInfos,
	})
}

func (s *Server) getK8sVersion() string {
	version, err := s.K8sClient.Discovery().ServerVersion()
	if err != nil {
		logf.Log.Error(err, "Failed to get k8s version")
		return "unknown"
	}
	return version.GitVersion
}

func (s *Server) getNodeMetricsMap(ctx context.Context) map[string]corev1.ResourceList {
	nodeMetricsMap := make(map[string]corev1.ResourceList)
	if s.MetricsClient == nil {
		return nodeMetricsMap
	}
	nmList, err := s.MetricsClient.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		logf.Log.Error(err, "Failed to list node metrics")
		return nodeMetricsMap
	}
	for _, nm := range nmList.Items {
		nodeMetricsMap[nm.Name] = nm.Usage
	}
	return nodeMetricsMap
}

func (s *Server) getPodRequestsPerNode(ctx context.Context) (map[string]*resource.Quantity, map[string]*resource.Quantity) {
	nodeReqCPU := make(map[string]*resource.Quantity)
	nodeReqMem := make(map[string]*resource.Quantity)

	pods, err := s.K8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		logf.Log.Error(err, "Failed to list pods for calculating node capacity requests")
		return nodeReqCPU, nodeReqMem
	}

	for _, pod := range pods.Items {
		if pod.Spec.NodeName == "" || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}

		reqCPU, reqMem := calculatePodRequests(pod)

		if _, ok := nodeReqCPU[pod.Spec.NodeName]; !ok {
			nodeReqCPU[pod.Spec.NodeName] = resource.NewQuantity(0, resource.DecimalSI)
			nodeReqMem[pod.Spec.NodeName] = resource.NewQuantity(0, resource.BinarySI)
		}
		nodeReqCPU[pod.Spec.NodeName].Add(*reqCPU)
		nodeReqMem[pod.Spec.NodeName].Add(*reqMem)
	}
	return nodeReqCPU, nodeReqMem
}

// calculatePodRequests returns the effective requests of a pod: the sum of its containers,
// or the largest init container if that is bigger, matching how the scheduler counts.
func calculatePodRequests(pod corev1.Pod) (*resource.Quantity, *resource.Quantity) {
	reqCPU := resource.NewQuantity(0, resource.DecimalSI)
	reqMem := resource.NewQuantity(0, resource.BinarySI)

	for _, container := range pod.Spec.Containers {
		if q, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
			reqCPU.Add(q)
		}
		if q, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
			reqMem.Add(q)
		}
	}

	for _, container := range pod.Spec.InitContainers {
		if q, ok := container.Resources.Requests[corev1.ResourceCPU]; ok && q.Cmp(*reqCPU) > 0 {
			reqCPU = &q
		}
		if q, ok := container.Resources.Requests[corev1.ResourceMemory]; ok && q.Cmp(*reqMem) > 0 {
			reqMem = &q
		}
	}
	return reqCPU, reqMem
}

func (s *Server) gatherNodeInfo(n corev1.Node, nodeMetricsMap map[string]corev1.ResourceList, nodeReqCPU, nodeReqMem map[string]*resource.Quantity) map[string]any {
	capacity := n.Status.Allocatable
	var uCPU, uMem resource.Quantity
	if usage, ok := nodeMetricsMap[n.Name]; ok {
		uCPU = *usage.Cpu()
		uMem = *usage.Memory()
	}

	var rCPU, rMem resource.Quantity
	if q, ok := nodeReqCPU[n.Name]; ok {
		rCPU = *q
	}
	if q, ok := nodeReqMem[n.Name]; ok {
		rMem = *q
	}

	status := "Unknown"
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			if cond.Status == corev1.ConditionTrue {
				status = "Ready"
			} else {
				status = "NotReady"
			}
		}
	}

	return map[string]any{
		"name":   n.Name,
		"status": status,
		"cpu": map[string]any{
			"used":      uCPU.AsApproximateFloat64(),
			"requested": rCPU.AsApproximateFloat64(),
			"capacity":  capacity.Cpu().AsApproximateFloat64(),
		},
		"mem": map[string]any{
			"used":      uMem.Value(),
			"requested": rMem.Value(),
			"capacity":  capacity.Memory().Value(),
		},
		"info": map[string]string{
			"os":      n.Status.NodeInfo.OSImage,
			"arch":    n.Status.NodeInfo.Architecture,
			"kernel":  n.Status.NodeInfo.KernelVersion,
			"kubelet": n.Status.NodeInfo.KubeletVersion,
		},
	}
}

func (s *Server) handleOperatorHealth(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	podName := os.Getenv("HOSTNAME")
	podNs := os.Getenv("POD_NAMESPACE")

	usageCPU := float64(0)
	usageMem := float64(m.Alloc / 1024 / 1024)
	var reqCPU, reqMem, limCPU, limMem float64

	if podName != "" && podNs != "" {
		if pod, err := s.K8sClient.CoreV1().Pods(podNs).Get(r.Context(), podName, metav1.GetOptions{}); err == nil {
			for _, container := range pod.Spec.Containers {
				reqCPU += float64(container.Resources.Requests.Cpu().MilliValue()) / 1000.0
				reqMem += float64(container.Resources.Requests.Memory().Value()) / 1024 / 1024
				limCPU += float64(container.Resources.Limits.Cpu().MilliValue()) / 1000.0
				limMem += float64(container.Resources.Limits.Memory().Value()) / 1024 / 1024
			}
		}

		if s.MetricsClient != nil {
			if podMetrics, err := s.MetricsClient.MetricsV1beta1().PodMetricses(podNs).Get(r.Context(), podName, metav1.GetOptions{}); err == nil {
				var totalCPU, totalMem int64
				for _, container := range podMetrics.Containers {
					totalCPU += container.Usage.Cpu().MilliValue()
					totalMem += container.Usage.Memory().Value()
				}
				usageCPU = float64(totalCPU) / 1000.0
				usageMem = float64(totalMem) / 1024 / 1024
			}
		}
	}

	var list finopsv1.NamespaceFinOpsList
	managedNamespaces := 0
	if err := s.Client.List(r.Context(), &list); err == nil {
		managedNamespaces = len(list.Items)
	}

	health := map[string]any{
		"status":            "healthy",
		"managedNamespaces": managedNamespaces,
		"memoryUsage":       usageMem,
		"cpuUsage":          usageCPU,
		"memoryRequests":    reqMem,
		"memoryLimits":      limMem,
		"cpuRequests":       reqCPU,
		"cpuLimits":         limCPU,
		"goroutines":        runtime.NumGoroutine(),
		"cpuCores":          runtime.NumCPU(),
		"heapAllocMiB":      float64(m.HeapAlloc) / 1024 / 1024,
		"sysMemoryMiB":      float64(m.Sys) / 1024 / 1024,
		"gcCycles":          m.NumGC,
		"timestamp":         metav1.Now(),
	}

	s.healthMu.Lock()
	s.healthHistory = append(s.healthHistory, health)
	if len(s.healthHistory) > 60 {
		s.healthHistory = s.healthHistory[len(s.healthHistory)-60:]
	}
	history := append([]map[string]any(nil), s.healthHistory...)
	s.healthMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"current": health,
		"history": history,
	})
}

func (s *Server) handleOperatorLogs(w http.ResponseWriter, r *http.Request) {
	s.streamOperatorLogs(w, r, 100, false)
}

func (s *Server) handleOperatorLogsDownload(w http.ResponseWriter, r *http.Request) {
	s.streamOperatorLogs(w, r, 0, true)
}

func (s *Server) streamOperatorLogs(w http.ResponseWriter, r *http.Request, tail int64, attachment bool) {
	podName := os.Getenv("HOSTNAME")
	podNs := os.Getenv("POD_NAMESPACE")
	if podName == "" || podNs == "" {
		writeError(w, http.StatusInternalServerError, "Operator environment not detected (HOSTNAME/POD_NAMESPACE missing)")
		return
	}

	opts := &corev1.PodLogOptions{}
	if tail > 0 {
		opts.TailLines = &tail
	}
	logs, err := s.K8sClient.CoreV1().Pods(podNs).GetLogs(podName, opts).DoRaw(r.Context())
	if err != nil {
		writeErrorf(w, http.StatusInternalServerError, "Failed to fetch logs: %v", err)
		return
	}

	if attachment {
		w.Header().Set("Content-Disposition", "attachment; filename=costdeck-operator.log")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(logs)
}

// handleDiscovery lists one resource type of one provider: GET /api/discovery/{provider}/{type}.
func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")
	resourceType := r.PathValue("type")
	if !slices.Contains(scaling.CloudProviders, providerName) {
		writeErrorf(w, http.StatusNotImplemented, "Provider '%s' is not supported", providerName)
		return
	}
	ctx := r.Context()
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings := scaling.CloudSettingsFor(cfg, providerName)
	if !settings.Enabled {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	provider, err := scaling.BuildProvider(ctx, s.Client, cfg, providerName, nil)
	if err != nil {
		logf.Log.Error(err, "Could not initialise cloud provider", "provider", providerName)
		writeError(w, http.StatusInternalServerError, "Cloud provider configuration error: "+err.Error())
		return
	}
	targets, err := provider.Discover(ctx, resourceType, settings.Filter)
	if err != nil {
		logf.Log.Error(err, "Could not discover resources", "provider", providerName, "type", resourceType)
		writeError(w, http.StatusInternalServerError, "Failed to discover external resources: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nonNil(targets))
}

// handleDiscoverAll lists every configured resource type of every enabled provider:
// GET /api/discovery. Providers that fail are reported in "errors" without hiding the rest.
func (s *Server) handleDiscoverAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	targets, errs := scaling.DiscoverAll(ctx, s.Client, cfg)
	messages := map[string]string{}
	for name, err := range errs {
		logf.Log.Error(err, "Could not discover resources", "provider", name)
		messages[name] = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": nonNil(targets), "errors": messages})
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}
