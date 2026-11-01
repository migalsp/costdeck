package metrics

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func TestPressureHistory(t *testing.T) {
	api := map[string]string{"pod": "api-7d9f8-abcde", "container": "api"}
	old := map[string]string{"pod": "api-7d9f8-fghij", "container": "api"}
	prom := &fakePromQL{answer: func(q string) []map[string]any {
		switch {
		case strings.HasPrefix(q, "quantile_over_time(0.95,") && strings.Contains(q, "container_cpu_cfs_throttled_periods_total"):
			return []map[string]any{sample(api, "0.31")}
		case strings.Contains(q, "kube_pod_container_status_last_terminated_reason"):
			return []map[string]any{sample(api, "1"), sample(old, "1")}
		case strings.Contains(q, "kube_pod_container_status_restarts_total"):
			return []map[string]any{sample(api, "2"), sample(old, "0")}
		case strings.Contains(q, "kube_pod_status_reason"):
			return []map[string]any{sample(map[string]string{"pod": "api-7d9f8-klmno"}, "1")}
		}
		return nil
	}}
	srv := httptest.NewServer(prom)
	defer srv.Close()
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.PressureHistory(context.Background(), "shop", 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if p := h.Containers[ContainerKey{Pod: "api-7d9f8-abcde", Container: "api"}]; p.ThrottledShare != 0.31 || !p.OOMKilled || p.Restarts != 2 {
		t.Errorf("api pressure = %+v", p)
	}
	if p := h.Containers[ContainerKey{Pod: "api-7d9f8-fghij", Container: "api"}]; p.OOMKilled {
		t.Errorf("an OOM kill with no restart in the window happened before it, got %+v", p)
	}
	if !h.EvictedPods["api-7d9f8-klmno"] || h.Window != 14*24*time.Hour {
		t.Errorf("evicted pods = %v, window = %v", h.EvictedPods, h.Window)
	}
	for _, q := range prom.queries {
		if !strings.Contains(q, `namespace="shop"`) || !strings.Contains(q, "14d") {
			t.Errorf("query %q must be scoped to the namespace and the window", q)
		}
	}
	if q := prom.queries[3]; strings.Contains(q, `container!=""`) || !strings.Contains(q, `reason="Evicted"`) {
		t.Errorf("evictions are pod-level and carry no container label: %q", q)
	}
}

func TestPressureHistoryQueries(t *testing.T) {
	prom := &fakePromQL{}
	srv := httptest.NewServer(prom)
	defer srv.Close()
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL, LabelSelector: `cluster="eu"`})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.PressureHistory(context.Background(), "shop", 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Containers) != 0 || len(h.EvictedPods) != 0 || h.Window != 7*24*time.Hour {
		t.Errorf("empty results = %+v, want empty maps and the window", h)
	}
	want := []string{
		`quantile_over_time(0.95, (sum by (pod, container) (rate(container_cpu_cfs_throttled_periods_total{namespace="shop",container!="",container!="POD",cluster="eu"}[5m])) / sum by (pod, container) (rate(container_cpu_cfs_periods_total{namespace="shop",container!="",container!="POD",cluster="eu"}[5m])))[7d:5m])`,
		`max by (pod, container) (max_over_time(kube_pod_container_status_last_terminated_reason{namespace="shop",container!="",container!="POD",reason="OOMKilled",cluster="eu"}[7d]))`,
		`max by (pod, container) (increase(kube_pod_container_status_restarts_total{namespace="shop",container!="",container!="POD",cluster="eu"}[7d]))`,
		`max by (pod) (max_over_time(kube_pod_status_reason{namespace="shop",reason="Evicted",cluster="eu"}[7d])) > 0`,
	}
	if len(prom.queries) != len(want) {
		t.Fatalf("queries = %q", prom.queries)
	}
	for i := range want {
		if prom.queries[i] != want[i] {
			t.Errorf("query %d = %q\nwant      %q", i, prom.queries[i], want[i])
		}
	}
}

func TestPressureHistoryNamesTheFailingQuery(t *testing.T) {
	for metric, want := range map[string]string{
		"container_cpu_cfs_throttled_periods_total":        "query CPU throttling",
		"kube_pod_container_status_last_terminated_reason": "query OOM kills",
		"kube_pod_container_status_restarts_total":         "query restarts",
		"kube_pod_status_reason":                           "query evictions",
	} {
		t.Run(metric, func(t *testing.T) {
			prom := &fakePromQL{failOn: func(q string) bool { return strings.Contains(q, metric+"{") }}
			srv := httptest.NewServer(prom)
			defer srv.Close()
			c, err := NewVMClient(VMOptions{Endpoint: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			h, err := c.PressureHistory(context.Background(), "shop", time.Hour)
			if h != nil || err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("PressureHistory() = %+v, %v; want an error naming %q", h, err, want)
			}
		})
	}
}

func TestProviderPressureHistory(t *testing.T) {
	prom := &fakePromQL{answer: func(q string) []map[string]any {
		if strings.Contains(q, "kube_pod_container_status_restarts_total") {
			return []map[string]any{sample(map[string]string{"pod": "api-1", "container": "api"}, "3")}
		}
		return nil
	}}
	srv := httptest.NewServer(prom)
	defer srv.Close()

	if h, err := NewProvider(configClient(t, nil), fixedSource{}).PressureHistory(context.Background(), "shop"); h != nil || err != nil {
		t.Errorf("without VictoriaMetrics PressureHistory() = %+v, %v; want nothing", h, err)
	}
	if len(prom.queries) != 0 {
		t.Fatalf("queries without VictoriaMetrics: %v", prom.queries)
	}
	vm := &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: srv.URL, RetentionDays: 2}
	h, err := NewProvider(configClient(t, vm), fixedSource{}).PressureHistory(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	if h.Window != 48*time.Hour || h.Containers[ContainerKey{Pod: "api-1", Container: "api"}].Restarts != 3 {
		t.Errorf("history = %+v, want the two days of retention and the restarts", h)
	}
	for _, q := range prom.queries {
		if !strings.Contains(q, "[2d") {
			t.Errorf("query %q must cover the retention window", q)
		}
	}
}
