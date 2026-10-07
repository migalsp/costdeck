package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// fakeRange answers range queries with one value per metric for the "shop" namespace at
// two timestamps, and records what it was asked.
type fakeRange struct {
	mu       sync.Mutex
	requests []map[string]string
	noKSM    bool
}

func (f *fakeRange) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.requests = append(f.requests, map[string]string{"query": q.Get("query"), "start": q.Get("start"), "end": q.Get("end"), "step": q.Get("step")})
	f.mu.Unlock()
	if !strings.HasSuffix(r.URL.Path, "/api/v1/query_range") {
		http.NotFound(w, r)
		return
	}
	query := q.Get("query")
	value := map[string]string{
		"container_cpu_usage_seconds_total":    "0.5",
		"container_memory_working_set_bytes":   "1073741824",
		"kube_pod_container_resource_requests": "2",
		"kube_pod_container_resource_limits":   "4",
	}
	result := []map[string]any{}
	for metric, v := range value {
		if !strings.Contains(query, metric+"{") || (f.noKSM && strings.HasPrefix(metric, "kube_")) {
			continue
		}
		if strings.Contains(query, `resource="memory"`) {
			v = map[string]string{"2": "2147483648", "4": "4294967296"}[v] // 2 GiB requested, 4 GiB limit
		}
		result = append(result, map[string]any{
			"metric": map[string]string{"namespace": "shop"},
			"values": []any{[]any{1700003600, v}, []any{1700000000, v}},
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": result}})
}

func TestNamespaceHistoryAveragesEachStep(t *testing.T) {
	prom := &fakeRange{}
	srv := httptest.NewServer(prom)
	defer srv.Close()
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL, LabelSelector: `cluster="eu"`})
	if err != nil {
		t.Fatal(err)
	}

	end := time.Unix(1700003600, 0)
	got, err := c.NamespaceHistory(context.Background(), end, 7*24*time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Every hour of the week gets a point; the fake has data for the last two only.
	points := got["shop"]
	if len(points) != 169 || !points[0].Timestamp.Before(&points[1].Timestamp) {
		t.Fatalf("want 169 hourly points in time order, got %d", len(points))
	}
	if first := points[0]; first.CPU.Usage != "0" || first.CPU.Requests != "0" || first.Memory.Usage != "0" {
		t.Errorf("a step without data must read as nothing running, got %+v", first)
	}
	p := points[len(points)-1]
	if p.CPU.Usage != "500m" || p.CPU.Requests != "2" || p.CPU.Limits != "4" {
		t.Errorf("CPU = %+v, want usage 500m, requests 2, limits 4", p.CPU)
	}
	if p.Memory.Usage != "1Gi" || p.Memory.Requests != "2Gi" || p.Memory.Limits != "4Gi" {
		t.Errorf("memory = %+v, want usage 1Gi, requests 2Gi, limits 4Gi", p.Memory)
	}

	if len(prom.requests) != 6 {
		t.Fatalf("want 6 range queries, got %d", len(prom.requests))
	}
	for _, r := range prom.requests {
		if r["step"] != "3600" || r["end"] != "1700003600" || r["start"] != "1699398800" {
			t.Errorf("query window = %s..%s step %s, want the 7 days before end at 3600s", r["start"], r["end"], r["step"])
		}
		if !strings.Contains(r["query"], `cluster="eu"`) || !strings.HasPrefix(r["query"], "sum by (namespace) (") {
			t.Errorf("query %q must sum by namespace within the label selector", r["query"])
		}
		switch {
		case strings.Contains(r["query"], "container_cpu_usage_seconds_total"):
			if !strings.Contains(r["query"], "[1h]") {
				t.Errorf("CPU must be the rate over each step: %s", r["query"])
			}
		case strings.Contains(r["query"], "container_memory_working_set_bytes"):
			if !strings.Contains(r["query"], "avg_over_time(") {
				t.Errorf("memory must be averaged over each step: %s", r["query"])
			}
		default:
			// Requests of completed pods (Jobs) are not reserved anymore.
			if !strings.Contains(r["query"], `kube_pod_status_phase{phase=~"Pending|Running",cluster="eu"} == 1`) {
				t.Errorf("allocation must only count pending and running pods: %s", r["query"])
			}
		}
	}
}

func TestNamespaceHistoryWithoutKubeStateMetrics(t *testing.T) {
	srv := httptest.NewServer(&fakeRange{noKSM: true})
	defer srv.Close()
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.NamespaceHistory(context.Background(), time.Unix(1700003600, 0), 24*time.Hour, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	points := got["shop"]
	p := points[len(points)-1]
	if p.CPU.Usage != "500m" || p.CPU.Requests != "" || p.Memory.Limits != "" || points[0].CPU.Requests != "" {
		t.Errorf("without kube-state-metrics want usage only, got %+v", p)
	}
}

func TestProviderNamespaceHistory(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	prom := &fakeRange{}
	srv := httptest.NewServer(prom)
	defer srv.Close()

	cfg := &finopsv1.CostDeckConfig{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cfg).Build()
	p := NewProvider(c, stubSource{})
	ctx := context.Background()

	if _, _, err := p.NamespaceHistory(ctx, 7*24*time.Hour); !errors.Is(err, ErrNoHistory) {
		t.Fatalf("without VictoriaMetrics want ErrNoHistory, got %v", err)
	}

	setVM(t, c, &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: srv.URL, RetentionDays: 3})
	p.TTL = 0
	got, span, err := p.NamespaceHistory(ctx, 7*24*time.Hour)
	if err != nil || len(got["shop"]) == 0 {
		t.Fatalf("got %v, %v", got, err)
	}
	if span != 3*24*time.Hour {
		t.Errorf("window = %v, want it capped at the 3 days of retentionDays", span)
	}
	if step := prom.requests[0]["step"]; step != "1800" {
		t.Errorf("step for 3 days = %ss, want 1800", step)
	}

	asked := len(prom.requests)
	if _, _, err := p.NamespaceHistory(ctx, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if len(prom.requests) != asked {
		t.Errorf("a second card within the cache TTL queried VictoriaMetrics again (%d -> %d requests)", asked, len(prom.requests))
	}
}

func TestHistoryStep(t *testing.T) {
	for window, want := range map[time.Duration]time.Duration{
		24 * time.Hour:      10 * time.Minute,
		7 * 24 * time.Hour:  time.Hour,
		14 * 24 * time.Hour: 2 * time.Hour,
		90 * 24 * time.Hour: 24 * time.Hour,
	} {
		if got := HistoryStep(window); got != want {
			t.Errorf("HistoryStep(%v) = %v, want %v", window, got, want)
		}
	}
}

func TestNamespaceHistoryFillsScaledDownSteps(t *testing.T) {
	// Usage at 00:00 and 03:00 only: in between the namespace was scaled to zero.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := []any{[]any{1700000000, "0.5"}, []any{1700010800, "0.5"}}
		result := []map[string]any{{"metric": map[string]string{"namespace": "shop"}, "values": values}}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": result}})
	}))
	defer srv.Close()
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.NamespaceHistory(context.Background(), time.Unix(1700014400, 0), 24*time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	points := got["shop"]
	if len(points) != 25 {
		t.Fatalf("want 25 hourly points for a day, got %d", len(points))
	}
	usage := make([]string, 0, 5)
	for _, p := range points[20:] {
		usage = append(usage, p.CPU.Usage+"/"+p.CPU.Requests)
	}
	// 00:00 … 04:00: the gap and the time after the last sample are zeros, not missing,
	// and so is the start of the window, before the first sample.
	want := []string{"500m/500m", "0/0", "0/0", "500m/500m", "0/0"}
	if strings.Join(usage, " ") != strings.Join(want, " ") || points[0].CPU.Usage != "0" {
		t.Errorf("usage/requests = %v (first %s), want %v after 19 zero steps", usage, points[0].CPU.Usage, want)
	}
}
