package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDCGM answers the GPU queries; exported says whether the scrape renamed the pod's
// namespace to exported_namespace.
func fakeDCGM(t *testing.T, exported bool) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
		var result []map[string]any
		sample := func(label, ns, v string) map[string]any {
			return map[string]any{"metric": map[string]string{label: ns}, "value": []any{1700000000, v}}
		}
		switch {
		case strings.HasPrefix(q, "count(") && exported:
			result = []map[string]any{{"metric": map[string]string{}, "value": []any{1700000000, "4"}}}
		case strings.HasPrefix(q, "count("):
		case strings.Contains(q, "DCGM_FI_DEV_GPU_UTIL{exported_namespace"):
			result = []map[string]any{sample("exported_namespace", "ml", "35")}
		case strings.Contains(q, "DCGM_FI_DEV_FB_USED{exported_namespace"):
			result = []map[string]any{sample("exported_namespace", "ml", "6144")}
		case strings.Contains(q, "DCGM_FI_DEV_GPU_UTIL{namespace"):
			result = []map[string]any{sample("namespace", "ml", "250"), sample("namespace", "vision", "4")}
		case strings.Contains(q, "DCGM_FI_DEV_FB_USED{namespace"):
			result = []map[string]any{sample("namespace", "vision", "2048")}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
	}))
	t.Cleanup(srv.Close)
	return srv, &queries
}

func TestGPUUsageFromDCGM(t *testing.T) {
	srv, queries := fakeDCGM(t, false)
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL, LabelSelector: `cluster="dev"`})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.GPUUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Utilization is a percentage, capped at 100; memory comes in MiB.
	if got["ml"].Utilization != 1 || got["vision"].Utilization != 0.04 || got["vision"].MemoryUsedGiB != 2 {
		t.Errorf("usage = %+v", got)
	}
	for _, q := range *queries {
		if !strings.Contains(q, `cluster="dev"`) {
			t.Errorf("query without the cluster's label selector: %s", q)
		}
	}
	if !strings.Contains(strings.Join(*queries, "\n"), `avg by (namespace) (avg_over_time(DCGM_FI_DEV_GPU_UTIL{namespace!="",cluster="dev"}[24h]))`) {
		t.Errorf("queries = %v", *queries)
	}
}

// Scraped by a ServiceMonitor that does not honour the exporter's labels, namespace is
// the exporter's own: the workload's is exported_namespace.
func TestGPUUsageUsesTheExportedNamespace(t *testing.T) {
	srv, queries := fakeDCGM(t, true)
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.GPUUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["ml"].Utilization != 0.35 || got["ml"].MemoryUsedGiB != 6 {
		t.Errorf("usage = %+v", got)
	}
	for _, q := range *queries {
		if strings.Contains(q, "{namespace") {
			t.Errorf("the exporter's namespace was queried: %s", q)
		}
	}
}
