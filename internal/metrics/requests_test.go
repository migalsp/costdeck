package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func TestNamespaceRequests(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		value := ""
		switch {
		case strings.HasPrefix(q, "count(") && strings.Contains(q, "envoy_cluster_upstream_rq_total"):
			value = "2"
		case strings.HasPrefix(q, "sum(increase(envoy_cluster_upstream_rq_total"):
			value = "41"
		case strings.HasPrefix(q, "count(") && strings.Contains(q, "istio_requests_total") && strings.Contains(q, `"busy"`):
			value = "1"
		case strings.HasPrefix(q, "sum(increase(istio_requests_total"):
			value = "1.5"
		}
		var result []map[string]any
		if value != "" {
			result = []map[string]any{{"metric": map[string]string{}, "value": []any{1700000000, value}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
	}))
	defer srv.Close()
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL, LabelSelector: `cluster="dev"`})
	if err != nil {
		t.Fatal(err)
	}
	n, known, err := c.NamespaceRequests(context.Background(), "busy", 2*time.Hour)
	if err != nil || !known || n != 42.5 {
		t.Errorf("busy = %v, %v, %v: Envoy and Istio both count", n, known, err)
	}
	all := strings.Join(queries, "\n")
	for _, want := range []string{
		`sum(increase(envoy_cluster_upstream_rq_total{envoy_cluster_name=~"httproute/busy/.*",cluster="dev"}[2h]))`,
		`count(last_over_time(istio_requests_total{reporter="source",destination_service_namespace="busy",cluster="dev"}[2h]))`,
		`count(last_over_time(nginx_ingress_controller_requests{namespace="busy",cluster="dev"}[2h]))`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no query %s in:\n%s", want, all)
		}
	}
	// No family knows a namespace: nothing says it is idle.
	queries = nil
	if _, known, _ := c.NamespaceRequests(context.Background(), "shop.dev", time.Hour); known {
		// Envoy's regex matches any namespace in this fake; the name must be quoted.
		if !strings.Contains(strings.Join(queries, "\n"), `httproute/shop\\.dev/`) {
			t.Errorf("the namespace is not quoted in the regex: %v", queries)
		}
	}
}

func TestNamespaceGPUPeak(t *testing.T) {
	exported := false
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		value := ""
		switch {
		case strings.Contains(q, `exported_namespace!=""`):
			if exported {
				value = "4"
			}
		case strings.HasPrefix(q, "count(last_over_time") && strings.Contains(q, `"train"`) && strings.Contains(q, "namespace="):
			value = "2"
		case strings.HasPrefix(q, "max(max_over_time") && strings.Contains(q, `"train"`):
			value = "37"
		}
		var result []map[string]any
		if value != "" {
			result = []map[string]any{{"metric": map[string]string{}, "value": []any{1700000000, value}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
	}))
	defer srv.Close()
	c, _ := NewVMClient(VMOptions{Endpoint: srv.URL})
	peak, known, err := c.NamespaceGPUPeak(context.Background(), "train", time.Hour)
	if err != nil || !known || peak != 37 {
		t.Errorf("train = %v, %v, %v", peak, known, err)
	}
	if !strings.Contains(strings.Join(queries, "\n"), `max(max_over_time(DCGM_FI_DEV_GPU_UTIL{namespace="train"}[1h]))`) {
		t.Errorf("queries = %v", queries)
	}
	if _, known, _ := c.NamespaceGPUPeak(context.Background(), "web", time.Hour); known {
		t.Error("a namespace without GPUs is known")
	}
	exported, queries = true, nil
	_, _, _ = c.NamespaceGPUPeak(context.Background(), "train", time.Hour)
	if !strings.Contains(strings.Join(queries, "\n"), `DCGM_FI_DEV_GPU_UTIL{exported_namespace="train"}`) {
		t.Errorf("a renamed label is not used: %v", queries)
	}
}

func TestProviderNamespaceRequestsAndGPUPeakNeedVictoriaMetrics(t *testing.T) {
	ctx := context.Background()
	disabled := NewProvider(configClient(t, nil), fixedSource{})
	if n, known, err := disabled.NamespaceRequests(ctx, "shop", time.Hour); n != 0 || known || !errors.Is(err, ErrNoHistory) {
		t.Errorf("NamespaceRequests() without VictoriaMetrics = %v, %v, %v; want ErrNoHistory", n, known, err)
	}
	if peak, known, err := disabled.NamespaceGPUPeak(ctx, "shop", time.Hour); peak != 0 || known || !errors.Is(err, ErrNoHistory) {
		t.Errorf("NamespaceGPUPeak() without VictoriaMetrics = %v, %v, %v; want ErrNoHistory", peak, known, err)
	}
	broken := NewProvider(configClient(t, &finopsv1.VictoriaMetricsConfig{Enabled: true}), fixedSource{})
	if _, _, err := broken.NamespaceRequests(ctx, "shop", time.Hour); err == nil || errors.Is(err, ErrNoHistory) {
		t.Errorf("misconfigured NamespaceRequests() error = %v, want the configuration error", err)
	}
	if _, _, err := broken.NamespaceGPUPeak(ctx, "shop", time.Hour); err == nil || errors.Is(err, ErrNoHistory) {
		t.Errorf("misconfigured NamespaceGPUPeak() error = %v, want the configuration error", err)
	}

	prom := &fakePromQL{answer: func(q string) []map[string]any {
		switch {
		case strings.HasPrefix(q, "count(last_over_time(nginx_ingress_controller_requests"):
			return []map[string]any{sample(nil, "1")}
		case strings.HasPrefix(q, "sum(increase(nginx_ingress_controller_requests"):
			return []map[string]any{sample(nil, "17")}
		case strings.HasPrefix(q, "count(last_over_time(DCGM_FI_DEV_GPU_UTIL{pod_namespace="):
			return []map[string]any{sample(nil, "1")}
		case strings.HasPrefix(q, "max(max_over_time(DCGM_FI_DEV_GPU_UTIL{pod_namespace="):
			return []map[string]any{sample(nil, "88")}
		}
		return nil
	}}
	srv := httptest.NewServer(prom)
	defer srv.Close()
	p := NewProvider(configClient(t, &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: srv.URL}), fixedSource{})
	if n, known, err := p.NamespaceRequests(ctx, "shop", 30*time.Minute); n != 17 || !known || err != nil {
		t.Errorf("NamespaceRequests() = %v, %v, %v; want the ingress-nginx count", n, known, err)
	}
	if peak, known, err := p.NamespaceGPUPeak(ctx, "shop", 30*time.Minute); peak != 88 || !known || err != nil {
		t.Errorf("NamespaceGPUPeak() = %v, %v, %v; want the pod_namespace peak", peak, known, err)
	}
	all := strings.Join(prom.queries, "\n")
	for _, want := range []string{
		`sum(increase(nginx_ingress_controller_requests{namespace="shop"}[30m]))`,
		`max(max_over_time(DCGM_FI_DEV_GPU_UTIL{pod_namespace="shop"}[30m]))`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no query %s in:\n%s", want, all)
		}
	}
}

func TestNamespaceRequestsAndGPUPeakFailOnAnyQueryError(t *testing.T) {
	tests := []struct {
		name   string
		failOn func(string) bool
		run    func(*VMClient) (float64, bool, error)
	}{
		{
			name:   "requests: a later family fails after an earlier one counted",
			failOn: func(q string) bool { return strings.Contains(q, "istio_requests_total") },
			run: func(c *VMClient) (float64, bool, error) {
				return c.NamespaceRequests(context.Background(), "shop", time.Hour)
			},
		},
		{
			name:   "requests: the sum fails after the count",
			failOn: func(q string) bool { return strings.HasPrefix(q, "sum(increase(envoy") },
			run: func(c *VMClient) (float64, bool, error) {
				return c.NamespaceRequests(context.Background(), "shop", time.Hour)
			},
		},
		{
			name:   "GPU peak: the exported-namespace probe fails",
			failOn: func(q string) bool { return strings.Contains(q, `exported_namespace!=""`) },
			run: func(c *VMClient) (float64, bool, error) {
				return c.NamespaceGPUPeak(context.Background(), "shop", time.Hour)
			},
		},
		{
			name:   "GPU peak: the maximum fails",
			failOn: func(q string) bool { return strings.HasPrefix(q, "max(max_over_time(") },
			run: func(c *VMClient) (float64, bool, error) {
				return c.NamespaceGPUPeak(context.Background(), "shop", time.Hour)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prom := &fakePromQL{failOn: tt.failOn, answer: func(q string) []map[string]any {
				if strings.HasPrefix(q, "count(last_over_time(") {
					return []map[string]any{sample(nil, "1")}
				}
				return []map[string]any{sample(nil, "5")}
			}}
			srv := httptest.NewServer(prom)
			defer srv.Close()
			c, err := NewVMClient(VMOptions{Endpoint: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			v, known, err := tt.run(c)
			if v != 0 || known || err == nil || !strings.Contains(err.Error(), "HTTP 500") {
				t.Errorf("got %v, %v, %v; want zero, unknown and the error", v, known, err)
			}
		})
	}
}
