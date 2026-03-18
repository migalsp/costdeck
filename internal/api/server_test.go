package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func buildMockServerWithK8s() *Server {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))

	client := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
	k8sClient := fake.NewSimpleClientset()

	k8sClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.35.0",
		Platform:   "linux/amd64",
	}

	return &Server{
		Client:    client,
		K8sClient: k8sClient,
	}
}

func TestHandleOperatorHealth(t *testing.T) {
	os.Setenv("HOSTNAME", "costdeck-operator-1234")
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("HOSTNAME")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServerWithK8s()

	req, err := http.NewRequest("GET", "/api/operator/health", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.handleOperatorHealth)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}

	current, ok := response["current"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected 'current' object in response")
	}

	if current["status"] != "healthy" {
		t.Errorf("expected status 'healthy', got %v", current["status"])
	}
}

func TestHandleClusterInfo(t *testing.T) {
	server := buildMockServerWithK8s()

	req, err := http.NewRequest("GET", "/api/cluster-info", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.handleClusterInfo)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var info map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}

	if info["version"] != "v1.35.0" {
		t.Errorf("expected version 'v1.35.0', got %v", info["version"])
	}
}

func TestHandleNamespaces(t *testing.T) {
	server := buildMockServerWithK8s()

	ns := &finopsv1.NamespaceFinOps{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Namespace: "costdeck"},
	}
	server.Client.Create(context.Background(), ns)

	req, err := http.NewRequest("GET", "/api/namespaces", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(server.handleNamespaces)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var parsed []finopsv1.NamespaceFinOps
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}

	if len(parsed) != 1 || parsed[0].Name != "test-ns" {
		t.Errorf("expected 1 namespace, got %v", parsed)
	}
}

func TestHandleDiscovery(t *testing.T) {
	server := buildMockServerWithK8s()

	// Test 1: Unsupported provider
	req, _ := http.NewRequest("GET", "/api/discovery/gcp/aurora", nil)
	rr := httptest.NewRecorder()
	server.handleDiscovery(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 Not Implemented for gcp, got %v", rr.Code)
	}

	// Test 2: AWS disabled
	os.Setenv("AWS_PROVIDER_ENABLED", "false")
	defer os.Unsetenv("AWS_PROVIDER_ENABLED")

	req, _ = http.NewRequest("GET", "/api/discovery/aws/aurora", nil)
	rr = httptest.NewRecorder()
	server.handleDiscovery(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for disabled AWS, got %v", rr.Code)
	}

	var parsed []interface{}
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 0 {
		t.Errorf("expected empty array for disabled AWS, got %v", parsed)
	}
}

func TestServeHistory(t *testing.T) {
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServerWithK8s()

	ns := &finopsv1.NamespaceFinOps{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Namespace: "costdeck"},
		Status: finopsv1.NamespaceFinOpsStatus{
			History: []finopsv1.MetricDataPoint{
				{Timestamp: metav1.Now(), CPU: finopsv1.ResourceMetrics{Usage: "100m"}},
			},
		},
	}
	server.Client.Create(context.Background(), ns)

	req, _ := http.NewRequest("GET", "/api/namespaces/test-ns/history", nil)
	rr := httptest.NewRecorder()
	server.handleNamespaceRouting(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", rr.Code)
	}

	var parsed []finopsv1.MetricDataPoint
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 || parsed[0].CPU.Usage != "100m" {
		t.Errorf("expected history data, got %v", parsed)
	}
}

func TestServePods(t *testing.T) {
	server := buildMockServerWithK8s()

	req, _ := http.NewRequest("GET", "/api/namespaces/test-ns/pods", nil)
	rr := httptest.NewRecorder()
	server.handleNamespaceRouting(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", rr.Code)
	}

	var parsed []PodDetail
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 0 {
		t.Errorf("expected 0 pods, got %d", len(parsed))
	}
}

func TestServeWorkloads(t *testing.T) {
	server := buildMockServerWithK8s()

	req, _ := http.NewRequest("GET", "/api/namespaces/test-ns/workloads", nil)
	rr := httptest.NewRecorder()
	server.handleNamespaceRouting(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", rr.Code)
	}

	var parsed []WorkloadDetail
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 0 {
		t.Errorf("expected 0 workloads, got %d", len(parsed))
	}
}

func TestHandleNamespaceOptimize(t *testing.T) {
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServerWithK8s()

	// Pre-create the required finops object
	nsFinOps := &finopsv1.NamespaceFinOps{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Namespace: "costdeck"},
		Status: finopsv1.NamespaceFinOpsStatus{
			History: []finopsv1.MetricDataPoint{
				{Timestamp: metav1.Now(), CPU: finopsv1.ResourceMetrics{Usage: "100m"}},
			},
		},
	}
	server.Client.Create(context.Background(), nsFinOps)

	req, _ := http.NewRequest("POST", "/api/namespaces/test-ns/optimize", nil)
	rr := httptest.NewRecorder()
	server.handleNamespaceRouting(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 InternalsServerError when no metrics client exists, got %v", rr.Code)
	}
}

func TestHandleNamespaceRevert(t *testing.T) {
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServerWithK8s()

	// Pre-create the required finops object
	opt := &finopsv1.NamespaceOptimization{
		ObjectMeta: metav1.ObjectMeta{Name: "test-ns", Namespace: "costdeck"},
		Status: finopsv1.NamespaceOptimizationStatus{
			Workloads: []finopsv1.WorkloadOptimization{
				{
					Name: "test-deploy",
					Kind: "Deployment",
					Original: finopsv1.ResourceValues{
						CPURequest: "100m",
					},
				},
			},
		},
	}
	server.Client.Create(context.Background(), opt)

	req, _ := http.NewRequest("POST", "/api/namespaces/test-ns/revert", nil)
	rr := httptest.NewRecorder()
	server.handleNamespaceRouting(rr, req)

	// Will likely return Ok as finding no deployment gracefully skips
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", rr.Code)
	}
}

func TestHandleScalingGroups(t *testing.T) {
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServerWithK8s()

	group := &finopsv1.ScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: "test-group", Namespace: "costdeck"},
		Spec: finopsv1.ScalingGroupSpec{
			Active: new(bool),
		},
	}
	server.Client.Create(context.Background(), group)

	req, _ := http.NewRequest("GET", "/api/scaling/groups", nil)
	rr := httptest.NewRecorder()
	server.handleScalingGroups(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", rr.Code)
	}

	var parsed []finopsv1.ScalingGroup
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 || parsed[0].Name != "test-group" {
		t.Errorf("expected 1 scaling group, got %v", len(parsed))
	}
}

func TestHandleScalingConfigs(t *testing.T) {
	os.Setenv("POD_NAMESPACE", "costdeck")
	defer os.Unsetenv("POD_NAMESPACE")

	server := buildMockServerWithK8s()

	config := &finopsv1.ScalingConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config", Namespace: "costdeck"},
		Spec: finopsv1.ScalingConfigSpec{
			Active: new(bool),
		},
	}
	server.Client.Create(context.Background(), config)

	req, _ := http.NewRequest("GET", "/api/scaling/configs", nil)
	rr := httptest.NewRecorder()
	server.handleScalingConfigs(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", rr.Code)
	}

	var parsed []finopsv1.ScalingConfig
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 || parsed[0].Name != "test-config" {
		t.Errorf("expected 1 scaling config, got %v", len(parsed))
	}
}

func TestHandleClusterNodes(t *testing.T) {
	server := buildMockServerWithK8s()

	// Add a dummy node
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
			NodeInfo: corev1.NodeSystemInfo{
				OSImage:        "Ubuntu",
				Architecture:   "amd64",
				KernelVersion:  "5.15",
				KubeletVersion: "v1.28.0",
			},
		},
	}
	server.K8sClient.CoreV1().Nodes().Create(context.Background(), node, metav1.CreateOptions{})

	req, _ := http.NewRequest("GET", "/api/cluster/nodes", nil)
	rr := httptest.NewRecorder()
	server.handleClusterNodes(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", rr.Code)
	}

	var parsed map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}

	nodes, ok := parsed["nodes"].([]interface{})
	if !ok || len(nodes) != 1 {
		t.Errorf("expected 1 node in response, got %v", parsed)
	}
}
