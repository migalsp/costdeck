package api

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"runtime"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// Version is set at build time via ldflags
var Version = "dev"

type Server struct {
	Client        client.Client
	K8sClient     kubernetes.Interface
	MetricsClient metricsv.Interface
	Port          string
	history       []map[string]interface{}
}

//go:embed ui/*
var uiFS embed.FS

//go:embed openapi.yaml
var openapiSpec []byte

func (s *Server) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("api-server")

	mux := http.NewServeMux()

	mux.HandleFunc("/api/namespaces", s.handleNamespaces)
	mux.HandleFunc("/api/namespaces/", s.handleNamespaceRouting)
	mux.HandleFunc("/api/cluster-info", s.handleClusterInfo)
	mux.HandleFunc("/api/operator/health", s.handleOperatorHealth)
	mux.HandleFunc("/api/operator/logs", s.handleOperatorLogs)
	mux.HandleFunc("/api/operator/logs/download", s.handleOperatorLogsDownload)
	mux.HandleFunc("/api/scaling/groups", s.handleScalingGroups)
	mux.HandleFunc("/api/scaling/groups/", s.handleScalingGroupActions)
	mux.HandleFunc("/api/scaling/configs", s.handleScalingConfigs)
	mux.HandleFunc("/api/scaling/configs/", s.handleScalingConfigActions)
	mux.HandleFunc("/api/discovery/", s.handleDiscovery)
	mux.HandleFunc("/api/version", s.handleVersion)
	mux.HandleFunc("/api/cluster/nodes", s.handleClusterNodes)
	mux.HandleFunc("/api/login", HandleLogin)
	mux.HandleFunc("/api/logout", HandleLogout)
	mux.HandleFunc("/api/openapi.yaml", handleOpenAPISpec)
	mux.HandleFunc("/api/docs", handleSwaggerUI)

	// Setup embedded filesystem for React UI
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		return err
	}
	fileServer := http.FileServer(http.FS(sub))
	mux.Handle("/", fileServer)

	// Wrap with auth middleware
	handler := AuthMiddleware(mux)

	addr := ":" + s.Port
	if s.Port == "" {
		addr = ":8082"
	}

	server := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	log.Info("Starting API server", "addr", addr)

	go func() {
		<-ctx.Done()
		log.Info("Shutting down API server")
		server.Shutdown(context.Background())
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}

func (s *Server) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var list finopsv1.NamespaceFinOpsList
	if err := s.Client.List(r.Context(), &list); err != nil {
		logf.Log.Error(err, "Failed to list NamespaceFinOps")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	logf.Log.Info("Found NamespaceFinOps", "count", len(list.Items))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list.Items)
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	// Expected path: /api/discovery/{provider}/{resourceType}
	if len(parts) < 5 {
		http.Error(w, "Invalid path format. Expected /api/discovery/{provider}/{type}", http.StatusBadRequest)
		return
	}

	providerName := parts[3]
	resourceType := parts[4]

	// Currently only "aws" is implemented, but we design for extension
	if providerName != "aws" {
		http.Error(w, fmt.Sprintf("Provider '%s' not supported yet", providerName), http.StatusNotImplemented)
		return
	}

	if os.Getenv("AWS_PROVIDER_ENABLED") != "true" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]interface{}{})
		return
	}

	// Initialize Provider (ideally cached or part of engine)
	awsProv, err := scaling.NewAWSProvider(r.Context())
	if err != nil {
		logf.Log.Error(err, "Failed to initialize AWS Discovery provider")
		http.Error(w, "Cloud provider configuration error", http.StatusInternalServerError)
		return
	}

	targets, err := awsProv.Discover(r.Context(), resourceType)
	if err != nil {
		logf.Log.Error(err, "Failed to discover resources", "provider", providerName, "type", resourceType)
		http.Error(w, "Failed to discover external resources", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(targets)
}

func (s *Server) handleNamespaceRouting(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	// Expected paths:
	// /api/namespaces/{ns}/history
	// /api/namespaces/{ns}/pods
	if len(parts) < 5 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	nsName := parts[3]
	action := parts[4]

	switch action {
	case "history":
		s.serveHistory(w, r, nsName)
	case "pods":
		s.servePods(w, r, nsName)
	case "workloads":
		if len(parts) >= 6 {
			s.serveWorkloadAction(w, r, nsName, parts[5])
		} else {
			s.serveWorkloads(w, r, nsName)
		}
	case "optimize":
		s.handleNamespaceOptimize(w, r, nsName)
	case "revert":
		s.handleNamespaceRevert(w, r, nsName)
	case "optimization":
		s.handleNamespaceOptimizationInfo(w, r, nsName)
	default:
		http.Error(w, "Invalid action", http.StatusBadRequest)
	}
}

func (s *Server) serveHistory(w http.ResponseWriter, r *http.Request, nsName string) {
	operatorNs := os.Getenv("POD_NAMESPACE")
	if operatorNs == "" {
		operatorNs = "costdeck"
	}

	var nsFinOps finopsv1.NamespaceFinOps
	if err := s.Client.Get(r.Context(), client.ObjectKey{Name: nsName, Namespace: operatorNs}, &nsFinOps); err != nil {
		if errors.IsNotFound(err) {
			// Fallback: try to find by targetNamespace field
			var list finopsv1.NamespaceFinOpsList
			if err := s.Client.List(r.Context(), &list); err == nil {
				found := false
				for _, item := range list.Items {
					if item.Spec.TargetNamespace == nsName {
						nsFinOps = item
						found = true
						break
					}
				}
				if !found {
					http.Error(w, "Not found", http.StatusNotFound)
					return
				}
			} else {
				http.Error(w, "Not found", http.StatusNotFound)
				return
			}
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(nsFinOps.Status.History)
}

type PodDetail struct {
	Name   string                   `json:"name"`
	Status string                   `json:"status"`
	CPU    finopsv1.ResourceMetrics `json:"cpu"`
	Memory finopsv1.ResourceMetrics `json:"memory"`
}

func (s *Server) servePods(w http.ResponseWriter, r *http.Request, nsName string) {
	ctx := r.Context()

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
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

		cpuU, _ := podMetricsMapCPU[p.Name]
		memU, _ := podMetricsMapMem[p.Name]
		if cpuU == "" {
			cpuU = "0"
		}
		if memU == "" {
			memU = "0"
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
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(details)
}

func (s *Server) handleClusterNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	k8sVer := s.getK8sVersion()

	nodes, err := s.K8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		http.Error(w, "Failed to list nodes: "+err.Error(), http.StatusInternalServerError)
		return
	}

	nodeMetricsMap := s.getNodeMetricsMap(ctx)
	nodeReqCPU, nodeReqMem := s.getPodRequestsPerNode(ctx)

	var totalCapacityCPU, totalCapacityMem resource.Quantity
	var totalUsageCPU, totalUsageMem resource.Quantity
	var totalRequestedCPU, totalRequestedMem resource.Quantity
	var nodeInfos []map[string]interface{}

	for _, n := range nodes.Items {
		info := s.gatherNodeInfo(n, nodeMetricsMap, nodeReqCPU, nodeReqMem)
		nodeInfos = append(nodeInfos, info)

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

	response := map[string]interface{}{
		"k8sVersion": k8sVer,
		"totalCapacity": map[string]interface{}{
			"cpu": totalCapacityCPU.AsApproximateFloat64(),
			"mem": totalCapacityMem.Value(),
		},
		"totalUsage": map[string]interface{}{
			"cpu": totalUsageCPU.AsApproximateFloat64(),
			"mem": totalUsageMem.Value(),
		},
		"totalRequested": map[string]interface{}{
			"cpu": totalRequestedCPU.AsApproximateFloat64(),
			"mem": totalRequestedMem.Value(),
		},
		"nodes": nodeInfos,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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

		reqCPU, reqMem := s.calculatePodRequests(pod)

		if _, ok := nodeReqCPU[pod.Spec.NodeName]; !ok {
			nodeReqCPU[pod.Spec.NodeName] = resource.NewQuantity(0, resource.DecimalSI)
			nodeReqMem[pod.Spec.NodeName] = resource.NewQuantity(0, resource.BinarySI)
		}
		nodeReqCPU[pod.Spec.NodeName].Add(*reqCPU)
		nodeReqMem[pod.Spec.NodeName].Add(*reqMem)
	}
	return nodeReqCPU, nodeReqMem
}

func (s *Server) calculatePodRequests(pod corev1.Pod) (*resource.Quantity, *resource.Quantity) {
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
		if q, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
			if q.Cmp(*reqCPU) > 0 {
				reqCPU = &q
			}
		}
		if q, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
			if q.Cmp(*reqMem) > 0 {
				reqMem = &q
			}
		}
	}
	return reqCPU, reqMem
}

func (s *Server) gatherNodeInfo(n corev1.Node, nodeMetricsMap map[string]corev1.ResourceList, nodeReqCPU, nodeReqMem map[string]*resource.Quantity) map[string]interface{} {
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

	return map[string]interface{}{
		"name":   n.Name,
		"status": status,
		"cpu": map[string]interface{}{
			"used":      uCPU.AsApproximateFloat64(),
			"requested": rCPU.AsApproximateFloat64(),
			"capacity":  capacity.Cpu().AsApproximateFloat64(),
		},
		"mem": map[string]interface{}{
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

func (s *Server) handleClusterInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	version, err := s.K8sClient.Discovery().ServerVersion()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	info := map[string]string{
		"version":  version.GitVersion,
		"platform": version.Platform,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}
func (s *Server) handleOperatorHealth(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	podName := os.Getenv("HOSTNAME")
	podNs := os.Getenv("POD_NAMESPACE")

	usageCPU := float64(0)
	usageMem := float64(m.Alloc / 1024 / 1024)
	reqCPU := float64(0)
	reqMem := float64(0)
	limCPU := float64(0)
	limMem := float64(0)

	if podName != "" && podNs != "" {
		// 1. Get Pod for requests/limits
		if pod, err := s.K8sClient.CoreV1().Pods(podNs).Get(r.Context(), podName, metav1.GetOptions{}); err == nil {
			for _, container := range pod.Spec.Containers {
				reqCPU += float64(container.Resources.Requests.Cpu().MilliValue()) / 1000.0
				reqMem += float64(container.Resources.Requests.Memory().Value()) / 1024 / 1024
				limCPU += float64(container.Resources.Limits.Cpu().MilliValue()) / 1000.0
				limMem += float64(container.Resources.Limits.Memory().Value()) / 1024 / 1024
			}
		}

		// 2. Get Pod Metrics for real usage (if metrics client available)
		if s.MetricsClient != nil {
			if podMetrics, err := s.MetricsClient.MetricsV1beta1().PodMetricses(podNs).Get(r.Context(), podName, metav1.GetOptions{}); err == nil {
				totalCPU := int64(0)
				totalMem := int64(0)
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

	health := map[string]interface{}{
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

	s.history = append(s.history, health)
	if len(s.history) > 60 {
		s.history = s.history[1:]
	}

	response := map[string]interface{}{
		"current": health,
		"history": s.history,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handleOperatorLogs(w http.ResponseWriter, r *http.Request) {
	podName := os.Getenv("HOSTNAME")
	podNs := os.Getenv("POD_NAMESPACE")
	if podName == "" || podNs == "" {
		http.Error(w, "Operator environment not detected (HOSTNAME/POD_NAMESPACE missing)", http.StatusInternalServerError)
		return
	}

	tailLines := int64(100)
	req := s.K8sClient.CoreV1().Pods(podNs).GetLogs(podName, &corev1.PodLogOptions{
		TailLines: &tailLines,
	})

	logs, err := req.DoRaw(r.Context())
	if err != nil {
		http.Error(w, "Failed to fetch logs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write(logs)
}

func (s *Server) handleOperatorLogsDownload(w http.ResponseWriter, r *http.Request) {
	podName := os.Getenv("HOSTNAME")
	podNs := os.Getenv("POD_NAMESPACE")
	if podName == "" || podNs == "" {
		http.Error(w, "Operator environment not detected", http.StatusInternalServerError)
		return
	}

	req := s.K8sClient.CoreV1().Pods(podNs).GetLogs(podName, &corev1.PodLogOptions{})
	logs, err := req.DoRaw(r.Context())
	if err != nil {
		http.Error(w, "Failed to fetch logs", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=costdeck-operator.log")
	w.Header().Set("Content-Type", "text/plain")
	w.Write(logs)
}

type WorkloadDetail struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Replicas      int32  `json:"replicas"`
	ReadyReplicas int32  `json:"readyReplicas"`
	Status        string `json:"status"` // running, scaled-down
}

func (s *Server) serveWorkloads(w http.ResponseWriter, r *http.Request, nsName string) {
	ctx := r.Context()
	result := []WorkloadDetail{}

	deployments := &appsv1.DeploymentList{}
	if err := s.Client.List(ctx, deployments, client.InNamespace(nsName)); err == nil {
		for _, d := range deployments.Items {
			replicas := int32(1)
			if d.Spec.Replicas != nil {
				replicas = *d.Spec.Replicas
			}
			status := "running"
			if replicas == 0 {
				status = "scaled-down"
			}
			result = append(result, WorkloadDetail{
				Name:          d.Name,
				Kind:          "Deployment",
				Replicas:      replicas,
				ReadyReplicas: d.Status.ReadyReplicas,
				Status:        status,
			})
		}
	}

	statefulSets := &appsv1.StatefulSetList{}
	if err := s.Client.List(ctx, statefulSets, client.InNamespace(nsName)); err == nil {
		for _, s := range statefulSets.Items {
			replicas := int32(1)
			if s.Spec.Replicas != nil {
				replicas = *s.Spec.Replicas
			}
			status := "running"
			if replicas == 0 {
				status = "scaled-down"
			}
			result = append(result, WorkloadDetail{
				Name:          s.Name,
				Kind:          "StatefulSet",
				Replicas:      replicas,
				ReadyReplicas: s.Status.ReadyReplicas,
				Status:        status,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) serveWorkloadAction(w http.ResponseWriter, r *http.Request, nsName string, workloadName string) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	var req struct {
		Kind     string `json:"kind"`
		Replicas int32  `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch req.Kind {
	case "Deployment":
		deploy := &appsv1.Deployment{}
		if err := s.Client.Get(ctx, client.ObjectKey{Name: workloadName, Namespace: nsName}, deploy); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		deploy.Spec.Replicas = &req.Replicas
		if err := s.Client.Update(ctx, deploy); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case "StatefulSet":
		ss := &appsv1.StatefulSet{}
		if err := s.Client.Get(ctx, client.ObjectKey{Name: workloadName, Namespace: nsName}, ss); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		ss.Spec.Replicas = &req.Replicas
		if err := s.Client.Update(ctx, ss); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "Unknown kind", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleNamespaceOptimize(w http.ResponseWriter, r *http.Request, nsName string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	operatorNs := getOperatorNamespace()

	// 1. Calculate Average Usage from NamespaceFinOps (last 60 mins)
	avgCpuNs, avgMemNs, err := s.calculateAverageUsage(ctx, nsName, operatorNs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 2. Get current individual usage from Metrics API
	currentCpuNs, currentMemNs, workloadUsage, workloadMemUsage, err := s.getCurrentUsage(ctx, nsName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 3. Compute Correction Factor
	cpuFactor := s.computeFactor(avgCpuNs, currentCpuNs)
	memFactor := s.computeFactor(avgMemNs, currentMemNs)

	// 4. Update Workloads and Store Optimization Info
	optimizedWorkloads := s.optimizeWorkloads(ctx, nsName, cpuFactor, memFactor, workloadUsage, workloadMemUsage)

	// 5. Store/Update NamespaceOptimization CR
	if err := s.updateOptimizationCR(ctx, nsName, operatorNs, optimizedWorkloads); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) calculateAverageUsage(ctx context.Context, nsName, operatorNs string) (float64, float64, error) {
	var finOps finopsv1.NamespaceFinOps
	if err := s.Client.Get(ctx, client.ObjectKey{Name: nsName, Namespace: operatorNs}, &finOps); err != nil {
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
		return 0, 0, nil, nil, fmt.Errorf("Metrics API is not available")
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
		if or.Kind == "ReplicaSet" {
			var rs appsv1.ReplicaSet
			if err := s.Client.Get(ctx, client.ObjectKey{Name: or.Name, Namespace: nsName}, &rs); err == nil {
				for _, rsor := range rs.OwnerReferences {
					if rsor.Kind == "Deployment" {
						return rsor.Name, "Deployment"
					}
				}
			}
		} else if or.Kind == "StatefulSet" {
			return or.Name, "StatefulSet"
		}
	}
	return "", ""
}

func (s *Server) computeFactor(avg, current float64) float64 {
	if current > 0 {
		return avg / current
	}
	return 1.0
}

func (s *Server) optimizeWorkloads(ctx context.Context, nsName string, cpuFactor, memFactor float64, workloadUsage, workloadMemUsage map[string]float64) []finopsv1.WorkloadOptimization {
	var optimizedWorkloads []finopsv1.WorkloadOptimization

	// Deployments
	deploys := &appsv1.DeploymentList{}
	s.Client.List(ctx, deploys, client.InNamespace(nsName))
	for _, d := range deploys.Items {
		if opt, ok := s.optimizeSingleWorkload(ctx, &d, "Deployment", cpuFactor, memFactor, workloadUsage, workloadMemUsage); ok {
			optimizedWorkloads = append(optimizedWorkloads, opt)
		}
	}

	// StatefulSets
	stss := &appsv1.StatefulSetList{}
	s.Client.List(ctx, stss, client.InNamespace(nsName))
	for _, sts := range stss.Items {
		if opt, ok := s.optimizeSingleWorkload(ctx, &sts, "StatefulSet", cpuFactor, memFactor, workloadUsage, workloadMemUsage); ok {
			optimizedWorkloads = append(optimizedWorkloads, opt)
		}
	}

	return optimizedWorkloads
}

func (s *Server) optimizeSingleWorkload(ctx context.Context, obj client.Object, kind string, cpuFactor, memFactor float64, workloadUsage, workloadMemUsage map[string]float64) (finopsv1.WorkloadOptimization, bool) {
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
		return finopsv1.WorkloadOptimization{}, false
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

	newReqCPU = s.applyFloor(newReqCPU, currentReqCPU, cpuFloor)
	newLimCPU = s.applyFloor(newLimCPU, currentLimCPU, cpuFloor*1.5)
	newReqMem = s.applyFloor(newReqMem, currentReqMem, memFloor)
	newLimMem = s.applyFloor(newLimMem, currentLimMem, memFloor*1.5)

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

	container.Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", int64(newReqCPU*1000))),
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", int64(newReqMem/1024/1024))),
	}
	container.Resources.Limits = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%dm", int64(newLimCPU*1000))),
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", int64(newLimMem/1024/1024))),
	}

	s.Client.Update(ctx, obj)

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
	}, true
}

func (s *Server) applyFloor(newValue, current, floor float64) float64 {
	if newValue < floor {
		if current >= floor {
			return floor
		}
		return current
	}
	return newValue
}

func (s *Server) updateOptimizationCR(ctx context.Context, nsName, operatorNs string, workloads []finopsv1.WorkloadOptimization) error {
	opt := &finopsv1.NamespaceOptimization{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nsName,
			Namespace: operatorNs,
		},
	}

	err := s.Client.Get(ctx, client.ObjectKey{Name: nsName, Namespace: operatorNs}, opt)
	opt.Spec.TargetNamespace = nsName

	if err != nil {
		if createErr := s.Client.Create(ctx, opt); createErr != nil {
			return fmt.Errorf("failed to create NamespaceOptimization: %w", createErr)
		}
	}

	opt.Status.Active = true
	opt.Status.OptimizedAt = metav1.Now()
	opt.Status.Workloads = workloads

	if statusErr := s.Client.Status().Update(ctx, opt); statusErr != nil {
		return fmt.Errorf("failed to update NamespaceOptimization status: %w", statusErr)
	}
	return nil
}

func (s *Server) handleNamespaceRevert(w http.ResponseWriter, r *http.Request, nsName string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	operatorNs := getOperatorNamespace()

	var opt finopsv1.NamespaceOptimization
	if err := s.Client.Get(ctx, client.ObjectKey{Name: nsName, Namespace: operatorNs}, &opt); err != nil {
		http.Error(w, "Optimization info not found", http.StatusNotFound)
		return
	}

	for _, w := range opt.Status.Workloads {
		if w.Kind == "Deployment" {
			deploy := &appsv1.Deployment{}
			if err := s.Client.Get(ctx, client.ObjectKey{Name: w.Name, Namespace: nsName}, deploy); err == nil {
				if len(deploy.Spec.Template.Spec.Containers) > 0 {
					deploy.Spec.Template.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(w.Original.CPURequest),
						corev1.ResourceMemory: resource.MustParse(w.Original.MemoryRequest),
					}
					deploy.Spec.Template.Spec.Containers[0].Resources.Limits = corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(w.Original.CPULimit),
						corev1.ResourceMemory: resource.MustParse(w.Original.MemoryLimit),
					}
					s.Client.Update(ctx, deploy)
				}
			}
		} else if w.Kind == "StatefulSet" {
			sts := &appsv1.StatefulSet{}
			if err := s.Client.Get(ctx, client.ObjectKey{Name: w.Name, Namespace: nsName}, sts); err == nil {
				if len(sts.Spec.Template.Spec.Containers) > 0 {
					sts.Spec.Template.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(w.Original.CPURequest),
						corev1.ResourceMemory: resource.MustParse(w.Original.MemoryRequest),
					}
					sts.Spec.Template.Spec.Containers[0].Resources.Limits = corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(w.Original.CPULimit),
						corev1.ResourceMemory: resource.MustParse(w.Original.MemoryLimit),
					}
					s.Client.Update(ctx, sts)
				}
			}
		}
	}

	opt.Status.Active = false
	s.Client.Status().Update(ctx, &opt)

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleNamespaceOptimizationInfo(w http.ResponseWriter, r *http.Request, nsName string) {
	ctx := r.Context()
	operatorNs := getOperatorNamespace()

	var opt finopsv1.NamespaceOptimization
	if err := s.Client.Get(ctx, client.ObjectKey{Name: nsName, Namespace: operatorNs}, &opt); err != nil {
		if errors.IsNotFound(err) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"active": false})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(opt.Status)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	version := strings.TrimPrefix(Version, "v")
	json.NewEncoder(w).Encode(map[string]string{
		"version": version,
	})
}

func handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(openapiSpec)
}

func handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Cost Deck API — Swagger UI</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>
    body { margin: 0; background: #fafafa; }
    .swagger-ui .topbar { display: none; }
    .costdeck-header {
      background: linear-gradient(135deg, #0f172a, #1e293b);
      padding: 16px 32px;
      display: flex;
      align-items: center;
      gap: 12px;
    }
    .costdeck-header h1 {
      color: #fff;
      font: 700 20px/1 -apple-system, BlinkMacSystemFont, 'Segoe UI', system-ui, sans-serif;
      margin: 0;
      letter-spacing: -0.5px;
    }
    .costdeck-header span {
      color: #34d399;
      font: 800 10px/1 -apple-system, BlinkMacSystemFont, sans-serif;
      text-transform: uppercase;
      letter-spacing: 2px;
    }
    .costdeck-badge {
      background: #10b981;
      color: #fff;
      width: 32px; height: 32px;
      border-radius: 10px;
      display: flex;
      align-items: center;
      justify-content: center;
      font: 700 16px/1 sans-serif;
    }
  </style>
</head>
<body>
  <div class="costdeck-header">
    <div class="costdeck-badge">K</div>
    <div>
      <h1>Cost Deck</h1>
      <span>API Documentation</span>
    </div>
  </div>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({
      url: '/api/openapi.yaml',
      dom_id: '#swagger-ui',
      presets: [
        SwaggerUIBundle.presets.apis,
        SwaggerUIBundle.SwaggerUIStandalonePreset
      ],
      layout: 'BaseLayout',
      deepLinking: true,
      defaultModelsExpandDepth: 1,
    });
  </script>
</body>
</html>`))
}
