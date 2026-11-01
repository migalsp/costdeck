package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/metrics"
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

	// NamespaceOptimization status is written through the status subresource, exactly as
	// the real CRD requires.
	client := fakeclient.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&finopsv1.NamespaceOptimization{}).Build()
	k8sClient := fake.NewClientset()

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
	t.Setenv("HOSTNAME", "costdeck-operator-1234")
	t.Setenv("POD_NAMESPACE", "costdeck")

	server := buildMockServerWithK8s()

	req, err := http.NewRequest("GET", "/api/operator/health", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := server.routes()
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
	}

	var response map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}

	current, ok := response["current"].(map[string]any)
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
	handler := server.routes()
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
		Name: "test-ns", Namespace: "costdeck",
	}
	if err := server.Client.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest("GET", "/api/namespaces", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := server.routes()
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
	req, _ := http.NewRequest("GET", "/api/discovery/oracle/db", nil)
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("expected 501 Not Implemented for oracle, got %v", rr.Code)
	}

	// Azure and Google Cloud are supported; while disabled they discover nothing.
	for _, p := range []string{"/api/discovery/azure/vm", "/api/discovery/gcp/gce"} {
		rr = httptest.NewRecorder()
		server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "[]" {
			t.Errorf("GET %s = %d %s, want 200 []", p, rr.Code, rr.Body.String())
		}
	}
	rr = httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/discovery", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"resources":[]`) {
		t.Errorf("GET /api/discovery = %d %s", rr.Code, rr.Body.String())
	}

	// Test 2: AWS disabled
	t.Setenv("AWS_PROVIDER_ENABLED", "false")

	req, _ = http.NewRequest("GET", "/api/discovery/aws/aurora", nil)
	rr = httptest.NewRecorder()
	server.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK for disabled AWS, got %v", rr.Code)
	}

	var parsed []any
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 0 {
		t.Errorf("expected empty array for disabled AWS, got %v", parsed)
	}
}

func TestServeHistory(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")

	server := buildMockServerWithK8s()

	ns := &finopsv1.NamespaceFinOps{
		Name: "test-ns", Namespace: "costdeck",
		Status: finopsv1.NamespaceFinOpsStatus{
			History: []finopsv1.MetricDataPoint{
				{Timestamp: metav1.Now(), CPU: finopsv1.ResourceMetrics{Usage: "100m"}},
			},
		},
	}
	if err := server.Client.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "/api/namespaces/test-ns/history", nil)
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, req)

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
	if rr.Header().Get("X-History-Range") != "1h" || rr.Header().Get("X-History-Source") != historySourceCostDeck {
		t.Errorf("headers = %v, want the operator's last hour", rr.Header())
	}
}

func TestServeHistoryRange(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	recent := finopsv1.MetricDataPoint{Timestamp: metav1.Now(),
		CPU:    finopsv1.ResourceMetrics{Usage: "100m", Requests: "1", Limits: "2"},
		Memory: finopsv1.ResourceMetrics{Usage: "64Mi", Requests: "1Gi", Limits: "2Gi"}}
	if err := server.Client.Create(ctx, &finopsv1.NamespaceFinOps{
		Name: "shop", Namespace: "costdeck",
		Spec:   finopsv1.NamespaceFinOpsSpec{TargetNamespace: "shop"},
		Status: finopsv1.NamespaceFinOpsStatus{History: []finopsv1.MetricDataPoint{recent}},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck"}
	if err := server.Client.Create(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	server.Metrics = metrics.NewProvider(server.Client, nil)

	get := func(rng string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		server.routes().ServeHTTP(rr, httptest.NewRequest("GET", "/api/namespaces/shop/history?range="+rng, nil))
		return rr
	}

	if rr := get("7x"); rr.Code != http.StatusBadRequest {
		t.Errorf("range=7x: got %d, want 400", rr.Code)
	}
	if rr := get("7d"); rr.Code != http.StatusOK || rr.Header().Get("X-History-Range") != "1h" || rr.Header().Get("X-History-Source") != historySourceCostDeck {
		t.Errorf("without VictoriaMetrics range=7d must fall back to the last hour, got %d %v", rr.Code, rr.Header())
	}

	// VictoriaMetrics without kube-state-metrics: usage only.
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := []any{}
		if strings.Contains(r.URL.Query().Get("query"), "container_cpu_usage_seconds_total") {
			end, _ := strconv.Atoi(r.URL.Query().Get("end"))
			result = append(result, map[string]any{"metric": map[string]string{"namespace": "shop"}, "values": []any{[]any{end, "0.25"}}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": result}})
	}))
	defer prom.Close()
	cfg.Spec.Integrations.VictoriaMetrics = &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: prom.URL, RetentionDays: 3}
	if err := server.Client.Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	rr := get("7d")
	if rr.Header().Get("X-History-Range") != "3d" || rr.Header().Get("X-History-Source") != metrics.SourceVictoriaMetrics {
		t.Fatalf("headers = %v, want 3d from VictoriaMetrics (capped at retentionDays)", rr.Header())
	}
	var points []finopsv1.MetricDataPoint
	if err := json.NewDecoder(rr.Body).Decode(&points); err != nil {
		t.Fatal(err)
	}
	if len(points) != 145 {
		t.Fatalf("want 3 days of half-hour points (145), got %d", len(points))
	}
	if last := points[len(points)-1]; last.CPU.Usage != "250m" || last.CPU.Requests != "1" || last.Memory.Limits != "2Gi" {
		t.Errorf("last point = %+v, want VictoriaMetrics usage with the current requests and limits", last)
	}
}

func TestServePods(t *testing.T) {
	server := buildMockServerWithK8s()

	req, _ := http.NewRequest("GET", "/api/namespaces/test-ns/pods", nil)
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, req)

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
	server.routes().ServeHTTP(rr, req)

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

func TestHandleNamespaceRevert(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")

	server := buildMockServerWithK8s()

	// Pre-create the required finops object
	opt := &finopsv1.NamespaceOptimization{
		Name: "test-ns", Namespace: "costdeck",
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
	if err := server.Client.Create(context.Background(), opt); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("POST", "/api/namespaces/test-ns/revert", nil)
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, req)

	// Will likely return Ok as finding no deployment gracefully skips
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", rr.Code)
	}
}

func TestHandleScalingGroups(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")

	server := buildMockServerWithK8s()

	group := &finopsv1.ScalingGroup{
		Name: "test-group", Namespace: "costdeck",
		Spec: finopsv1.ScalingGroupSpec{
			Active: new(bool),
		},
	}
	if err := server.Client.Create(context.Background(), group); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "/api/scaling/groups", nil)
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, req)

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
	t.Setenv("POD_NAMESPACE", "costdeck")

	server := buildMockServerWithK8s()

	config := &finopsv1.ScalingConfig{
		Name: "test-config", Namespace: "costdeck",
		Spec: finopsv1.ScalingConfigSpec{
			Active: new(bool),
		},
	}
	if err := server.Client.Create(context.Background(), config); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "/api/scaling/configs", nil)
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, req)

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
		Name: "test-node",
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
	// The handler reads nodes through the cached client.
	if err := server.Client.Create(context.Background(), node); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", "/api/cluster/nodes", nil)
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %v", rr.Code)
	}

	var parsed map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}

	nodes, ok := parsed["nodes"].([]any)
	if !ok || len(nodes) != 1 {
		t.Errorf("expected 1 node in response, got %v", parsed)
	}
}

func TestOperatorAndAdminRoutesRefuseLowerRolesBeforeTheHandlerRuns(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	t.Setenv("HOSTNAME", "costdeck-operator-0")
	t.Setenv("COSTDECK_AUTH_USER", "admin")
	t.Setenv("COSTDECK_AUTH_PASSWORD", "a-long-enough-password")
	ctx := context.Background()
	calls := &handlerCalls{}
	server := interceptedServer(callsMadeByHandlers(calls))
	outgoing := &refusingTransport{}
	server.HTTPClient = &http.Client{Transport: outgoing}
	poster := &recordingPoster{}
	server.Poster = poster
	svc, err := auth.NewService(ctx, server.Client)
	if err != nil {
		t.Fatal(err)
	}
	server.Auth = svc
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	clientset := server.K8sClient.(*fake.Clientset)

	tokens := map[auth.Role]string{}
	for _, role := range []auth.Role{auth.RoleViewer, auth.RoleOperator, auth.RoleAdmin} {
		token, _, err := svc.Tokens.Create(ctx, string(role)+"-token", role, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		tokens[role] = token
	}
	issued := httptest.NewRecorder()
	if err := svc.Sessions.Issue(issued, httptest.NewRequest(http.MethodGet, "/", nil), auth.Identity{Subject: "oidc:vera", Name: "Vera", Role: auth.RoleViewer, Provider: "oidc"}); err != nil {
		t.Fatal(err)
	}
	viewerSession := issued.Result().Cookies()

	type caller struct {
		name    string
		role    auth.Role
		prepare func(*http.Request)
	}
	byToken := func(role auth.Role) caller {
		return caller{string(role) + " token", role, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tokens[role]) }}
	}
	sessionOfViewer := caller{"viewer session", auth.RoleViewer, func(r *http.Request) {
		for _, c := range viewerSession {
			r.AddCookie(c)
		}
	}}
	do := func(route registeredRoute, who caller) *httptest.ResponseRecorder {
		var body io.Reader
		if route.method != http.MethodGet && route.method != http.MethodDelete {
			body = strings.NewReader("{")
		}
		req := httptest.NewRequest(route.method, route.path(), body)
		who.prepare(req)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	observed := 0
	routes := routesNeeding(t, auth.RoleOperator, auth.RoleAdmin)
	if len(routes) < 30 {
		t.Fatalf("found only %d operator and admin routes in server.go; has the registration style changed?", len(routes))
	}
	for _, route := range routes {
		callers := []caller{byToken(auth.RoleViewer), sessionOfViewer}
		if route.role == auth.RoleAdmin {
			callers = append(callers, byToken(auth.RoleOperator))
		}
		for _, who := range callers {
			calls.take()
			clientset.ClearActions()
			outgoing.take()
			poster.posts = nil

			rr := do(route, who)
			want := "This action needs the " + string(route.role) + " role; you are signed in as " + string(who.role) + "."
			if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), want) {
				t.Errorf("%s %s as %s = %d %s, want 403 from the role check", route.method, route.pattern, who.name, rr.Code, rr.Body.String())
			}
			if made := calls.take(); len(made) > 0 {
				t.Errorf("%s %s as %s reached the handler, which called the API server: %v", route.method, route.pattern, who.name, made)
			}
			if actions := clientset.Actions(); len(actions) > 0 {
				t.Errorf("%s %s as %s reached the handler, which used the clientset: %v", route.method, route.pattern, who.name, actions)
			}
			if urls := outgoing.take(); len(urls) > 0 || len(poster.posts) > 0 {
				t.Errorf("%s %s as %s sent something out: %v %q", route.method, route.pattern, who.name, urls, poster.posts)
			}
		}

		calls.take()
		clientset.ClearActions()
		if rr := do(route, byToken(route.role)); rr.Code == http.StatusForbidden && strings.Contains(rr.Body.String(), "This action needs the") {
			t.Errorf("%s %s with the %s role it requires = %s, the role check must let it through", route.method, route.pattern, route.role, rr.Body.String())
		}
		observed += len(calls.take()) + len(clientset.Actions())
	}
	if observed == 0 {
		t.Error("handlers allowed to run left no trace, so the checks above could not see a handler run either")
	}
}
