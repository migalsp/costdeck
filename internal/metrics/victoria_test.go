package metrics

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
	failOn  func(query string) bool
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
	if f.failOn != nil && f.failOn(r.URL.Query().Get("query")) {
		http.Error(w, "query execution failed", http.StatusInternalServerError)
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
		{"http://10.0.3.7:8428", "http://10.0.3.7:8428", false},
		{"http://127.0.0.1:8428", "http://127.0.0.1:8428", false},
		{"vm:8428", "", true},
		{"ftp://vm", "", true},
		{"", "", true},
		// The cloud metadata services hand out the node's credentials.
		{"http://169.254.169.254/latest/meta-data", "", true},
		{"http://[fd00:ec2::254]/latest", "", true},
		{"http://[fe80::1]:8428", "", true},
		// Credentials belong in the Secret, not in the URL.
		{"http://admin:secret@vm:8428", "", true},
		{"http://vm:8428/ space", "", true},
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
func TestProviderFollowsConfigChangesAndFallsBack(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))

	srv := httptest.NewServer(&fakePromQL{answer: func(string) []map[string]any { return []map[string]any{sample(nil, "3")} }})
	defer srv.Close()

	cfg := &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck"}
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
		Name: "vm-creds", Namespace: "costdeck",
		Data: map[string][]byte{SecretKeyBearerToken: []byte("t")},
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

func TestContainerDemandQueriesP95AndPeak(t *testing.T) {
	prom := &fakePromQL{answer: func(q string) []map[string]any {
		api := map[string]string{"pod": "api-7d9f8-abcde", "container": "api"}
		switch {
		case strings.HasPrefix(q, "quantile_over_time(0.95,"):
			return []map[string]any{sample(api, "0.035")}
		case strings.HasPrefix(q, "max_over_time("):
			return []map[string]any{sample(api, "125829120"), sample(map[string]string{"pod": "api-7d9f8-abcde", "container": "sidecar"}, "1048576")}
		}
		return nil
	}}
	srv := httptest.NewServer(prom)
	defer srv.Close()
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	got, err := c.ContainerDemand(context.Background(), "shop", 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	api := got[ContainerKey{Pod: "api-7d9f8-abcde", Container: "api"}]
	if api.CPU.MilliValue() != 35 || api.Memory.Value() != 125829120 {
		t.Errorf("api demand = %s CPU, %s memory", api.CPU.String(), api.Memory.String())
	}
	if side := got[ContainerKey{Pod: "api-7d9f8-abcde", Container: "sidecar"}]; side.Memory.Value() != 1048576 {
		t.Errorf("a container with memory but no CPU sample must still be reported, got %+v", side)
	}
	for _, q := range prom.queries {
		if !strings.Contains(q, `namespace="shop"`) || !strings.Contains(q, "[7d:5m]") {
			t.Errorf("query %q must be scoped to the namespace and the window", q)
		}
	}
}

func TestVMClientReportsQueryFailures(t *testing.T) {
	longBody := strings.Repeat("x", 400)
	tests := []struct {
		name   string
		status int
		body   string
		want   []string
	}{
		{name: "server error with a long body", status: http.StatusBadGateway, body: longBody, want: []string{"query namespace CPU: ", "returned HTTP 502: " + strings.Repeat("x", 300) + "…"}},
		{name: "forbidden", status: http.StatusForbidden, body: "denied", want: []string{"rejected the credentials (HTTP 403)"}},
		{name: "PromQL error payload", status: http.StatusOK, body: `{"status":"error","errorType":"bad_data","error":"parse error at char 5"}`, want: []string{"query namespace CPU: query failed: bad_data: parse error at char 5"}},
		{name: "not a Prometheus payload", status: http.StatusOK, body: "<html>Grafana</html>", want: []string{"is not a Prometheus API payload"}},
		{name: "numeric sample value", status: http.StatusOK, body: `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,1.5]}]}}`, want: []string{"unexpected sample value type float64"}},
		{name: "unparseable sample value", status: http.StatusOK, body: `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"abc"]}]}}`, want: []string{`parse sample value "abc"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c, err := NewVMClient(VMOptions{Endpoint: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.NamespaceUsage(context.Background(), "shop")
			if err == nil {
				t.Fatal("NamespaceUsage() succeeded")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err, want)
				}
			}
			if strings.Contains(err.Error(), strings.Repeat("x", 301)) {
				t.Errorf("the response body was not truncated: %d bytes in the error", len(err.Error()))
			}
		})
	}
}

func TestSampleValueTreatsNaNAndInfAsZero(t *testing.T) {
	for in, want := range map[string]float64{"NaN": 0, "+Inf": 0, "-Inf": 0, "Inf": 0, "1e3": 1000, "0.25": 0.25} {
		got, err := sampleValue([2]any{1700000000, in})
		if err != nil || got != want {
			t.Errorf("sampleValue(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}

func TestVMClientKeepsEndpointParametersAndPrefersTheBearerToken(t *testing.T) {
	var path, extraLabel, query, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if query == "" {
			path, extraLabel, query, auth = r.URL.Path, r.URL.Query().Get("extra_label"), r.URL.Query().Get("query"), r.Header.Get("Authorization")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{}}})
	}))
	defer srv.Close()

	c, err := NewVMClient(VMOptions{Endpoint: srv.URL + "/select/0/prometheus/api/v1/query?extra_label=cluster%3Dprod", BearerToken: "tok", Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Name() != SourceVictoriaMetrics {
		t.Errorf("Name() = %q", c.Name())
	}
	if _, err := c.NamespaceUsage(context.Background(), "shop"); err != nil {
		t.Fatal(err)
	}
	if path != "/select/0/prometheus/api/v1/query" || extraLabel != "cluster=prod" || !strings.HasPrefix(query, "sum(rate(container_cpu_usage_seconds_total{") {
		t.Errorf("request path %q, extra_label %q, query %q", path, extraLabel, query)
	}
	if auth != "Bearer tok" {
		t.Errorf("Authorization = %q, want the bearer token to win over basic auth", auth)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"  short body \n", 20, "short body"},
		{"exact", 5, "exact"},
		{"abcdef", 3, "abc…"},
		{"", 3, ""},
	}
	for _, tt := range tests {
		if got := truncate(tt.in, tt.n); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

func TestVMClientTrustsTheConfiguredCA(t *testing.T) {
	srv := httptest.NewTLSServer(&fakePromQL{answer: func(string) []map[string]any { return []map[string]any{sample(nil, "1")} }})
	defer srv.Close()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	tests := []struct {
		name    string
		opts    VMOptions
		wantErr string
	}{
		{name: "unknown authority", opts: VMOptions{Endpoint: srv.URL}, wantErr: "certificate"},
		{name: "private CA", opts: VMOptions{Endpoint: srv.URL, CACert: caPEM}},
		{name: "verification skipped", opts: VMOptions{Endpoint: srv.URL, InsecureSkipVerify: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewVMClient(tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			err = c.Validate(context.Background())
			if tt.wantErr == "" && err != nil {
				t.Errorf("Validate() = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("Validate() = %v, want a TLS verification error", err)
			}
		})
	}

	if _, err := NewVMClient(VMOptions{Endpoint: srv.URL, CACert: []byte("not a certificate")}); err == nil || !strings.Contains(err.Error(), "CA certificate is not valid PEM") {
		t.Errorf("NewVMClient() with a broken CA = %v", err)
	}
}
