package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// fakePromQL answers instant queries with canned vector results and records the queries.
type fakePromQL struct {
	mu      sync.Mutex
	queries []string
	auth    []string
	answer  func(query string) []map[string]any
	status  int
}

func (f *fakePromQL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.queries = append(f.queries, r.URL.Query().Get("query"))
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()

	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/api/v1/query") {
		http.NotFound(w, r)
		return
	}
	result := []map[string]any{}
	if f.answer != nil {
		result = f.answer(r.URL.Query().Get("query"))
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "success",
		"data":   map[string]any{"resultType": "vector", "result": result},
	})
}

func sample(labels map[string]string, value string) map[string]any {
	return map[string]any{"metric": labels, "value": []any{1700000000, value}}
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"http://vm:8428", "http://vm:8428", false},
		{"http://vm:8428/", "http://vm:8428", false},
		{"  http://vm:8428/api/v1/query ", "http://vm:8428", false},
		{"http://vmselect:8481/select/0/prometheus/api/v1", "http://vmselect:8481/select/0/prometheus", false},
		{"https://vm.example.com/select/0/prometheus/?extra_label=cluster=a", "https://vm.example.com/select/0/prometheus?extra_label=cluster=a", false},
		{"vm:8428", "", true},
		{"ftp://vm", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		got, err := NormalizeEndpoint(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("NormalizeEndpoint(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if err == nil && got.String() != tt.want {
			t.Errorf("NormalizeEndpoint(%q) = %q, want %q", tt.in, got.String(), tt.want)
		}
	}
}

func TestVMClientQueriesAndAuth(t *testing.T) {
	prom := &fakePromQL{answer: func(q string) []map[string]any {
		switch {
		case strings.HasPrefix(q, "sum by (pod)") && strings.Contains(q, "cpu"):
			return []map[string]any{sample(map[string]string{"pod": "api-1"}, "0.25"), sample(map[string]string{"pod": "api-2"}, "NaN")}
		case strings.HasPrefix(q, "sum by (pod)"):
			return []map[string]any{sample(map[string]string{"pod": "api-1"}, "1048576"), sample(map[string]string{"pod": "worker"}, "2097152")}
		case strings.Contains(q, "cpu"):
			return []map[string]any{sample(nil, "1.5")}
		default:
			return []map[string]any{sample(nil, "536870912")}
		}
	}}
	srv := httptest.NewServer(prom)
	defer srv.Close()

	c, err := NewVMClient(VMOptions{Endpoint: srv.URL + "/", LabelSelector: `cluster="prod"`, BearerToken: "s3cr3t"})
	if err != nil {
		t.Fatal(err)
	}

	usage, err := c.NamespaceUsage(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if got := usage.CPU.MilliValue(); got != 1500 {
		t.Errorf("namespace CPU = %dm, want 1500m", got)
	}
	if got := usage.Memory.Value(); got != 512<<20 {
		t.Errorf("namespace memory = %d, want %d", got, 512<<20)
	}

	pods, err := c.PodUsage(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	api1, api2, worker := pods["api-1"], pods["api-2"], pods["worker"]
	if got := api1.CPU.MilliValue(); got != 250 {
		t.Errorf("api-1 CPU = %dm, want 250m", got)
	}
	if got := api2.CPU.MilliValue(); got != 0 {
		t.Errorf("NaN sample must read as zero, got %dm", got)
	}
	if got := worker.Memory.Value(); got != 2<<20 {
		t.Errorf("a pod that only has a memory series must still be reported, got %d", got)
	}

	for i, q := range prom.queries {
		if !strings.Contains(q, `namespace="shop"`) || !strings.Contains(q, `cluster="prod"`) || !strings.Contains(q, `container!="POD"`) {
			t.Errorf("query %d is missing a required matcher: %s", i, q)
		}
		if prom.auth[i] != "Bearer s3cr3t" {
			t.Errorf("query %d sent Authorization %q", i, prom.auth[i])
		}
	}
}

func TestVMClientNamespaceAverageUsesWindow(t *testing.T) {
	prom := &fakePromQL{answer: func(string) []map[string]any { return []map[string]any{sample(nil, "2")} }}
	srv := httptest.NewServer(prom)
	defer srv.Close()

	c, err := NewVMClient(VMOptions{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.NamespaceAverage(context.Background(), "shop", 14*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prom.queries[0], "avg_over_time(") || !strings.Contains(prom.queries[0], "[14d:5m]") {
		t.Errorf("average query does not cover the configured window: %s", prom.queries[0])
	}
}

func TestVMClientValidateDiagnostics(t *testing.T) {
	t.Run("connected without cAdvisor series", func(t *testing.T) {
		srv := httptest.NewServer(&fakePromQL{})
		defer srv.Close()
		c, _ := NewVMClient(VMOptions{Endpoint: srv.URL})
		err := c.Validate(context.Background())
		if err == nil || !strings.Contains(err.Error(), "no container_cpu_usage_seconds_total series") {
			t.Fatalf("Validate() = %v, want an explicit missing-metrics error", err)
		}
	})
	t.Run("cluster endpoint without tenant path", func(t *testing.T) {
		srv := httptest.NewServer(&fakePromQL{status: http.StatusNotFound})
		defer srv.Close()
		c, _ := NewVMClient(VMOptions{Endpoint: srv.URL})
		err := c.Validate(context.Background())
		if err == nil || !strings.Contains(err.Error(), "/select/<accountID>/prometheus") {
			t.Fatalf("Validate() = %v, want a hint about the vmselect tenant path", err)
		}
	})
	t.Run("rejected credentials", func(t *testing.T) {
		srv := httptest.NewServer(&fakePromQL{status: http.StatusUnauthorized})
		defer srv.Close()
		c, _ := NewVMClient(VMOptions{Endpoint: srv.URL, Username: "u", Password: "p"})
		if err := c.Validate(context.Background()); err == nil || !strings.Contains(err.Error(), "credentials") {
			t.Fatalf("Validate() = %v, want a credentials error", err)
		}
	})
}

func TestNewVMClientRejectsBadSelector(t *testing.T) {
	for _, sel := range []string{`cluster=prod`, `cluster="a"} or vector(1) # `, `{"a"}`} {
		if _, err := NewVMClient(VMOptions{Endpoint: "http://vm", LabelSelector: sel}); err == nil {
			t.Errorf("label selector %q was accepted", sel)
		}
	}
	for _, sel := range []string{`cluster="prod"`, `{cluster="prod", env=~"dev|qa"}`} {
		if _, err := NewVMClient(VMOptions{Endpoint: "http://vm", LabelSelector: sel}); err != nil {
			t.Errorf("label selector %q was rejected: %v", sel, err)
		}
	}
}

// stubSource is a metrics-server stand-in.
type stubSource struct{ cpuMilli int64 }

func (s stubSource) Name() string { return SourceMetricsServer }
func (s stubSource) NamespaceUsage(context.Context, string) (Usage, error) {
	return Usage{CPU: *resourceMilli(s.cpuMilli)}, nil
}
func (s stubSource) PodUsage(context.Context, string) (map[string]Usage, error) { return nil, nil }
func (s stubSource) NamespaceAverage(context.Context, string, time.Duration) (Usage, error) {
	return Usage{}, ErrUnsupported
}

func TestProviderFollowsConfigChangesAndFallsBack(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))

	srv := httptest.NewServer(&fakePromQL{answer: func(string) []map[string]any { return []map[string]any{sample(nil, "3")} }})
	defer srv.Close()

	cfg := &finopsv1.CostDeckConfig{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cfg).Build()
	p := NewProvider(c, stubSource{cpuMilli: 100})
	p.TTL = 0 // Re-resolve on every call so the test sees each config change.
	ctx := context.Background()

	if _, res, _ := p.NamespaceUsage(ctx, "shop"); res.Source != SourceMetricsServer || res.Degraded != nil {
		t.Fatalf("with VictoriaMetrics disabled got %+v", res)
	}

	// Enabling VictoriaMetrics must take effect without a restart.
	setVM(t, c, &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: srv.URL, SecretRef: "vm-creds"})
	if err := c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vm-creds", Namespace: "costdeck"},
		Data:       map[string][]byte{SecretKeyBearerToken: []byte("t")},
	}); err != nil {
		t.Fatal(err)
	}
	u, res, err := p.NamespaceUsage(ctx, "shop")
	if err != nil || res.Source != SourceVictoriaMetrics || u.CPU.MilliValue() != 3000 {
		t.Fatalf("after enabling VictoriaMetrics got usage=%v res=%+v err=%v", u.CPU.String(), res, err)
	}

	// A broken endpoint degrades to metrics-server and says why.
	setVM(t, c, &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: "http://127.0.0.1:1"})
	u, res, err = p.NamespaceUsage(ctx, "shop")
	if err != nil || res.Source != SourceMetricsServer || res.Degraded == nil || u.CPU.MilliValue() != 100 {
		t.Fatalf("with a broken endpoint got usage=%v res=%+v err=%v", u.CPU.String(), res, err)
	}
}

func setVM(t *testing.T, c client.Client, vm *finopsv1.VictoriaMetricsConfig) {
	t.Helper()
	cfg := &finopsv1.CostDeckConfig{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "default", Namespace: "costdeck"}, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Spec.Integrations.VictoriaMetrics = vm
	if err := c.Update(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}

func resourceMilli(m int64) *resource.Quantity {
	return resource.NewMilliQuantity(m, resource.DecimalSI)
}
