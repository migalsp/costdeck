package metrics

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
