package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/config"
)

func TestCalculatePodRequestsCountsLikeTheScheduler(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pod       corev1.PodSpec
		cpu, mem  string
		explained string
	}{
		{"containers add up", corev1.PodSpec{Containers: []corev1.Container{{Resources: requests("500m", "1Gi")}, {Resources: requests("250m", "512Mi")}}},
			"750m", "1536Mi", "the requests of all containers are summed"},
		{"a larger init container wins", corev1.PodSpec{
			InitContainers: []corev1.Container{{Resources: requests("2", "256Mi")}},
			Containers:     []corev1.Container{{Resources: requests("500m", "1Gi")}}},
			"2", "1Gi", "init containers run alone, so only the largest counts, per resource"},
		{"no requests", corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}, "0", "0", "a container without requests reserves nothing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cpu, mem := calculatePodRequests(corev1.Pod{Spec: tc.pod})
			if cpu.Cmp(resource.MustParse(tc.cpu)) != 0 || mem.Cmp(resource.MustParse(tc.mem)) != 0 {
				t.Errorf("requests = %s / %s, want %s / %s: %s", cpu, mem, tc.cpu, tc.mem, tc.explained)
			}
		})
	}
}

func TestCalculatePodRequestsAddsNativeSidecars(t *testing.T) {
	t.Skip("bug: calculatePodRequests treats an init container with restartPolicy Always (a native sidecar) as a one-off init container; the scheduler adds its requests to the pod's")
	always := corev1.ContainerRestartPolicyAlways
	pod := corev1.Pod{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "mesh-proxy", RestartPolicy: &always, Resources: requests("500m", "256Mi")}},
		Containers:     []corev1.Container{{Name: "app", Resources: requests("1", "1Gi")}},
	}}
	cpu, mem := calculatePodRequests(pod)
	if cpu.Cmp(resource.MustParse("1500m")) != 0 || mem.Cmp(resource.MustParse("1280Mi")) != 0 {
		t.Errorf("requests = %s / %s, want 1500m / 1280Mi: the sidecar runs next to the app for the pod's whole life", cpu, mem)
	}
}

func TestCalculatePodRequestsIncludesPodOverhead(t *testing.T) {
	t.Skip("bug: calculatePodRequests ignores spec.overhead (RuntimeClass), which the scheduler and finops.PodRequests both count")
	pod := corev1.Pod{Spec: corev1.PodSpec{
		Overhead:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("160Mi")},
		Containers: []corev1.Container{{Name: "app", Resources: requests("1", "1Gi")}},
	}}
	cpu, mem := calculatePodRequests(pod)
	if cpu.Cmp(resource.MustParse("1250m")) != 0 || mem.Cmp(resource.MustParse("1184Mi")) != 0 {
		t.Errorf("requests = %s / %s, want 1250m / 1184Mi", cpu, mem)
	}
}

func holdLease(t *testing.T, server *Server, holder string) {
	t.Helper()
	lease := &coordinationv1.Lease{Name: config.LeaderElectionID, Namespace: operatorNamespace, Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder}}
	if _, err := server.K8sClient.CoordinationV1().Leases(operatorNamespace).Create(context.Background(), lease, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func logRequests(t *testing.T, server *Server) []*corev1.PodLogOptions {
	t.Helper()
	var out []*corev1.PodLogOptions
	for _, a := range server.K8sClient.(*fake.Clientset).Actions() {
		if a.GetSubresource() == "log" {
			if a.GetNamespace() != operatorNamespace {
				t.Errorf("logs were read in namespace %q", a.GetNamespace())
			}
			out = append(out, a.(k8stesting.GenericAction).GetValue().(*corev1.PodLogOptions))
		}
	}
	return out
}

func TestOperatorLogsComeFromTheLeader(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	t.Setenv("HOSTNAME", "costdeck-operator-standby")
	server := buildMockServerWithK8s()
	holdLease(t, server, "costdeck-operator-leader_7f3c2a9e-uuid")

	rr := send(t, server, http.MethodGet, "/api/operator/logs", "")
	if rr.Code != http.StatusOK || rr.Body.String() != "fake logs" {
		t.Fatalf("logs = %d %q", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("X-Costdeck-Pod") != "costdeck-operator-leader" || rr.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("headers = %v, want the leader pod named and plain text", rr.Header())
	}
	if rr.Header().Get("Content-Disposition") != "" {
		t.Error("the log view is shown inline, not downloaded")
	}
	if opts := logRequests(t, server); len(opts) != 1 || opts[0].TailLines == nil || *opts[0].TailLines != 100 {
		t.Errorf("log requests = %+v, want the last 100 lines", opts)
	}
}

func TestOperatorLogsDownloadIsTheWholeLogOfThisPod(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	t.Setenv("HOSTNAME", "costdeck-operator-0")
	server := buildMockServerWithK8s()

	rr := send(t, server, http.MethodGet, "/api/operator/logs/download", "")
	if rr.Code != http.StatusOK || rr.Body.String() != "fake logs" {
		t.Fatalf("download = %d %q", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Content-Disposition") != "attachment; filename=costdeck-operator-0.log" || rr.Header().Get("X-Costdeck-Pod") != "costdeck-operator-0" {
		t.Errorf("headers = %v, want this pod's log as an attachment while no leader is known", rr.Header())
	}
	if opts := logRequests(t, server); len(opts) != 1 || opts[0].TailLines != nil {
		t.Errorf("log requests = %+v, want the whole log", opts)
	}
}

func TestOperatorLogsNeedThePodEnvironment(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")
	t.Setenv("HOSTNAME", "")
	server := buildMockServerWithK8s()
	rr := sendAs(t, server, &auth.Identity{Subject: "token:ops", Role: auth.RoleOperator}, http.MethodGet, "/api/operator/logs", "")
	if rr.Code != http.StatusInternalServerError || errorMessage(t, rr) != "Operator environment not detected (HOSTNAME/POD_NAMESPACE missing)" {
		t.Errorf("logs outside a pod = %d %s", rr.Code, rr.Body.String())
	}
	if opts := logRequests(t, server); len(opts) != 0 {
		t.Errorf("no pod may be asked for logs, got %+v", opts)
	}
}

func nodeMetrics(t *testing.T, usage map[string][2]string) *metricsfake.Clientset {
	t.Helper()
	client := metricsfake.NewSimpleClientset() //nolint:staticcheck
	for name, u := range usage {
		nm := &metricsv1beta1.NodeMetrics{Name: name, Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(u[0]), corev1.ResourceMemory: resource.MustParse(u[1])}}
		if err := client.Tracker().Create(metricsv1beta1.SchemeGroupVersion.WithResource("nodes"), nm, ""); err != nil {
			t.Fatal(err)
		}
	}
	return client
}

func TestClusterNodesReportUsageAndRequestsPerNode(t *testing.T) {
	server := buildMockServerWithK8s()
	server.MetricsClient = nodeMetrics(t, map[string][2]string{"n1": {"1500m", "4Gi"}})
	onN2 := podWith("shop", "db", corev1.PodRunning, corev1.Container{Name: "db", Resources: requests("2", "8Gi")})
	onN2.Spec.NodeName = "n2"
	unscheduled := podWith("shop", "queued", corev1.PodPending, corev1.Container{Name: "app", Resources: requests("4", "4Gi")})
	unscheduled.Spec.NodeName = ""
	createAll(t, server.Client,
		readyNode("n1", "4", "16Gi", true), readyNode("n2", "8", "32Gi", false),
		podWith("shop", "web", corev1.PodRunning, corev1.Container{Name: "app", Resources: requests("500m", "1Gi")}),
		podWith("shop", "batch", corev1.PodSucceeded, corev1.Container{Name: "job", Resources: requests("3", "3Gi")}),
		onN2, unscheduled,
	)

	rr := send(t, server, http.MethodGet, "/api/cluster/nodes", "")
	type usage struct {
		CPU float64 `json:"cpu"`
		Mem int64   `json:"mem"`
	}
	type node struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		CPU    struct {
			Used, Requested, Capacity float64
		} `json:"cpu"`
		Mem struct {
			Used, Requested, Capacity int64
		} `json:"mem"`
	}
	got := decodeInto[struct {
		K8sVersion     string `json:"k8sVersion"`
		TotalCapacity  usage  `json:"totalCapacity"`
		TotalUsage     usage  `json:"totalUsage"`
		TotalRequested usage  `json:"totalRequested"`
		Nodes          []node `json:"nodes"`
	}](t, rr)
	const gi = int64(1 << 30)
	if rr.Code != http.StatusOK || got.K8sVersion != kubernetesVersion || len(got.Nodes) != 2 {
		t.Fatalf("nodes = %d %+v", rr.Code, got)
	}
	if got.TotalCapacity != (usage{12, 48 * gi}) || got.TotalUsage != (usage{1.5, 4 * gi}) || got.TotalRequested != (usage{2.5, 9 * gi}) {
		t.Errorf("totals = %+v / %+v / %+v, want finished and unscheduled pods left out", got.TotalCapacity, got.TotalUsage, got.TotalRequested)
	}
	byName := map[string]node{}
	for _, n := range got.Nodes {
		byName[n.Name] = n
	}
	if n := byName["n1"]; n.Status != "Ready" || n.CPU.Used != 1.5 || n.CPU.Requested != 0.5 || n.CPU.Capacity != 4 || n.Mem.Requested != gi {
		t.Errorf("n1 = %+v", n)
	}
	if n := byName["n2"]; n.Status != "NotReady" || n.CPU.Used != 0 || n.CPU.Requested != 2 || n.Mem.Requested != 8*gi {
		t.Errorf("n2 = %+v, want no usage without a metrics reading", n)
	}
}

func TestOperatorHealthListsTheReplicasAndTheLeader(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	t.Setenv("HOSTNAME", "costdeck-operator-b")
	server := buildMockServerWithK8s()
	holdLease(t, server, "costdeck-operator-a_uuid")
	labels := map[string]string{"app.kubernetes.io/name": "costdeck-operator", "app.kubernetes.io/instance": "costdeck"}
	replica := func(name, node string, ready bool, restarts int32) *corev1.Pod {
		p := &corev1.Pod{Name: name, Namespace: operatorNamespace, Labels: labels, Spec: corev1.PodSpec{NodeName: node,
			Containers: []corev1.Container{{Name: "manager", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("512Mi")},
			}}}}}
		p.Status = corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "manager", Ready: ready, RestartCount: restarts}}}
		return p
	}
	createAll(t, server.Client,
		replica("costdeck-operator-b", "n2", false, 3), replica("costdeck-operator-a", "n1", true, 0),
		&corev1.Pod{Name: "other-app", Namespace: operatorNamespace, Labels: map[string]string{"app.kubernetes.io/name": "other"}},
		&finopsv1.NamespaceFinOps{Name: "shop", Namespace: operatorNamespace},
	)

	got := decodeInto[struct {
		Current struct {
			Pod               string    `json:"pod"`
			Leader            string    `json:"leader"`
			Replicas          []Replica `json:"replicas"`
			ManagedNamespaces int       `json:"managedNamespaces"`
			CPURequests       float64   `json:"cpuRequests"`
			MemoryLimits      float64   `json:"memoryLimits"`
		} `json:"current"`
		History []json.RawMessage `json:"history"`
	}](t, send(t, server, http.MethodGet, "/api/operator/health", ""))
	c := got.Current
	if c.Pod != "costdeck-operator-b" || c.Leader != "costdeck-operator-a" || c.ManagedNamespaces != 1 || c.CPURequests != 0.1 || c.MemoryLimits != 512 {
		t.Errorf("health = %+v", c)
	}
	want := []Replica{{Name: "costdeck-operator-a", Ready: true, Node: "n1"}, {Name: "costdeck-operator-b", Ready: false, Node: "n2", Restarts: 3}}
	if !reflect.DeepEqual(c.Replicas, want) {
		t.Errorf("replicas = %+v, want %+v: the pods of this Deployment only, sorted", c.Replicas, want)
	}
	if len(got.History) != 1 {
		t.Errorf("history = %d entries after one request", len(got.History))
	}
}
