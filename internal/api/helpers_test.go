package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/auth"
)

const (
	operatorNamespace = "costdeck"
	cpuCoreHourRate   = 0.04
	memoryGiBHourRate = 0.005
	customCurrency    = "EUR"
	customRatesBasis  = "Custom rates from CostDeckConfig"
	shopNamespace     = "shop"
	apiWorkload       = "api"
	webexMePath       = "/v1/people/me"
	kubernetesVersion = "v1.35.0"
)

func testScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	return scheme
}

func interceptedServer(funcs interceptor.Funcs) *Server {
	c := fakeclient.NewClientBuilder().WithScheme(testScheme()).
		WithStatusSubresource(&finopsv1.NamespaceOptimization{}).
		WithInterceptorFuncs(funcs).Build()
	clientset := fake.NewClientset()
	clientset.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: kubernetesVersion, Platform: "linux/amd64"}
	return &Server{Client: c, K8sClient: clientset}
}

func createAll(t *testing.T, c client.Client, objs ...client.Object) {
	t.Helper()
	for _, obj := range objs {
		if err := c.Create(context.Background(), obj); err != nil {
			t.Fatalf("creating %T %s: %v", obj, obj.GetName(), err)
		}
	}
}

func seedConfig(t *testing.T, server *Server, spec finopsv1.CostDeckConfigSpec) {
	t.Helper()
	createAll(t, server.Client, &finopsv1.CostDeckConfig{Name: "default", Namespace: operatorNamespace, Spec: spec})
}

func customPricing() finopsv1.PricingConfig {
	return finopsv1.PricingConfig{CPUCoreHour: "0.04", MemoryGBHour: "0.005", Currency: customCurrency}
}

func monthlyAtCustomRates(cores, gib float64) float64 {
	return (cores*cpuCoreHourRate + gib*memoryGiBHourRate) * 730
}

func closeTo(got, want float64) bool { return math.Abs(got-want) < 1e-6 }

func sendAs(t *testing.T, server *Server, id *auth.Identity, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if id != nil {
		req = req.WithContext(auth.WithIdentity(req.Context(), id))
	}
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, req)
	return rr
}

func decodeInto[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not the expected JSON (%v): %d %s", err, rr.Code, rr.Body.String())
	}
	return out
}

func errorMessage(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	return decodeInto[map[string]string](t, rr)["error"]
}

type failingPoster struct {
	err   error
	posts int
}

func (p *failingPoster) Post(context.Context, string) (string, error) {
	p.posts++
	return "", p.err
}

type capturedRequest struct {
	Path          string
	Authorization string
	Body          string
}

type requestLog struct {
	mu       sync.Mutex
	requests []capturedRequest
}

func (l *requestLog) add(r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, capturedRequest{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: string(body)})
}

func (l *requestLog) all() []capturedRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]capturedRequest(nil), l.requests...)
}

func recordingServer(t *testing.T, answer http.HandlerFunc) (*httptest.Server, *requestLog) {
	t.Helper()
	log := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		answer(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

func fakeWebexAPI(t *testing.T, handler http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

type handlerCalls struct {
	mu    sync.Mutex
	calls []string
}

func (h *handlerCalls) record(ctx context.Context, verb string, obj any) {
	if auth.FromContext(ctx) == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, fmt.Sprintf("%s %T", verb, obj))
}

func (h *handlerCalls) take() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.calls
	h.calls = nil
	return out
}

func callsMadeByHandlers(rec *handlerCalls) interceptor.Funcs {
	return interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			rec.record(ctx, "get", obj)
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			rec.record(ctx, "list", list)
			return c.List(ctx, list, opts...)
		},
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			rec.record(ctx, "create", obj)
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			rec.record(ctx, "update", obj)
			return c.Update(ctx, obj, opts...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			rec.record(ctx, "patch", obj)
			return c.Patch(ctx, obj, patch, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			rec.record(ctx, "delete", obj)
			return c.Delete(ctx, obj, opts...)
		},
		DeleteAllOf: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteAllOfOption) error {
			rec.record(ctx, "deleteAllOf", obj)
			return c.DeleteAllOf(ctx, obj, opts...)
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			rec.record(ctx, "update "+sub, obj)
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			rec.record(ctx, "patch "+sub, obj)
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	}
}

type refusingTransport struct {
	mu   sync.Mutex
	urls []string
}

func (rt *refusingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.urls = append(rt.urls, r.URL.String())
	return nil, fmt.Errorf("outgoing request to %s refused by the test", r.URL)
}

func (rt *refusingTransport) take() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := rt.urls
	rt.urls = nil
	return out
}

type registeredRoute struct {
	method, pattern string
	role            auth.Role
}

func routesNeeding(t *testing.T, roles ...auth.Role) []registeredRoute {
	t.Helper()
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []registeredRoute
	for _, m := range routePattern.FindAllStringSubmatch(string(src), -1) {
		if slices.Contains(roles, auth.Role(m[1])) {
			out = append(out, registeredRoute{method: m[2], pattern: m[3], role: auth.Role(m[1])})
		}
	}
	return out
}

var pathParameter = regexp.MustCompile(`\{[^}]+\}`)

func (r registeredRoute) path() string { return pathParameter.ReplaceAllString(r.pattern, "x") }

func requests(cpu, memory string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}}
}

func podWith(ns, name string, phase corev1.PodPhase, containers ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{Name: name, Namespace: ns, Spec: corev1.PodSpec{NodeName: "n1", Containers: containers}, Status: corev1.PodStatus{Phase: phase}}
}

func readyNode(name, cpu, memory string, ready bool) *corev1.Node {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	capacity := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(memory)}
	return &corev1.Node{Name: name, Status: corev1.NodeStatus{Capacity: capacity, Allocatable: capacity,
		Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}}}
}

func sameQuantity(got, want string) bool {
	g, err := resource.ParseQuantity(got)
	return err == nil && g.Cmp(resource.MustParse(want)) == 0
}

func namespaceFinOps(name, cpuRequests, cpuUsage, memoryRequests, memoryUsage string) *finopsv1.NamespaceFinOps {
	nf := &finopsv1.NamespaceFinOps{Name: name, Namespace: operatorNamespace, Spec: finopsv1.NamespaceFinOpsSpec{TargetNamespace: name}}
	if cpuRequests != "" {
		nf.Status.History = []finopsv1.MetricDataPoint{
			{CPU: finopsv1.ResourceMetrics{Requests: "99", Usage: "99"}, Memory: finopsv1.ResourceMetrics{Requests: "99Gi", Usage: "99Gi"}},
			{CPU: finopsv1.ResourceMetrics{Requests: cpuRequests, Usage: cpuUsage}, Memory: finopsv1.ResourceMetrics{Requests: memoryRequests, Usage: memoryUsage}},
		}
	}
	return nf
}
