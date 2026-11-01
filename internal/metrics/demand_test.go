package metrics

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func TestProviderContainerDemandWindowFollowsRetention(t *testing.T) {
	api := map[string]string{"pod": "api-1", "container": "api"}
	tests := []struct {
		name          string
		retentionDays int
		wantWindow    time.Duration
		wantCPU       string
		wantMemory    string
	}{
		{
			name: "default lookback", wantWindow: 7 * 24 * time.Hour,
			wantCPU:    `quantile_over_time(0.95, sum by (pod, container) (rate(container_cpu_usage_seconds_total{namespace="shop",container!="",container!="POD",cluster="eu"}[5m]))[7d:5m])`,
			wantMemory: `max_over_time(max by (pod, container) (container_memory_working_set_bytes{namespace="shop",container!="",container!="POD",cluster="eu"})[7d:5m])`,
		},
		{
			name: "short retention", retentionDays: 3, wantWindow: 3 * 24 * time.Hour,
			wantCPU:    `quantile_over_time(0.95, sum by (pod, container) (rate(container_cpu_usage_seconds_total{namespace="shop",container!="",container!="POD",cluster="eu"}[5m]))[3d:5m])`,
			wantMemory: `max_over_time(max by (pod, container) (container_memory_working_set_bytes{namespace="shop",container!="",container!="POD",cluster="eu"})[3d:5m])`,
		},
		{
			name: "long retention capped at two weeks", retentionDays: 30, wantWindow: 14 * 24 * time.Hour,
			wantCPU:    `quantile_over_time(0.95, sum by (pod, container) (rate(container_cpu_usage_seconds_total{namespace="shop",container!="",container!="POD",cluster="eu"}[5m]))[14d:5m])`,
			wantMemory: `max_over_time(max by (pod, container) (container_memory_working_set_bytes{namespace="shop",container!="",container!="POD",cluster="eu"})[14d:5m])`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prom := &fakePromQL{answer: func(q string) []map[string]any {
				if strings.HasPrefix(q, "quantile_over_time") {
					return []map[string]any{sample(api, "0.2")}
				}
				return []map[string]any{sample(api, "268435456")}
			}}
			srv := httptest.NewServer(prom)
			defer srv.Close()
			vm := &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: srv.URL, LabelSelector: `cluster="eu"`, RetentionDays: tt.retentionDays}
			p := NewProvider(configClient(t, vm), fixedSource{})

			got, demand, err := p.ContainerDemand(context.Background(), "shop")
			if err != nil {
				t.Fatal(err)
			}
			if demand != (Demand{Source: SourceVictoriaMetrics, Historical: true, Window: tt.wantWindow}) {
				t.Errorf("demand = %+v", demand)
			}
			u := got[ContainerKey{Pod: "api-1", Container: "api"}]
			if u.CPU.MilliValue() != 200 || u.Memory.Value() != 256<<20 {
				t.Errorf("api demand = %s CPU, %s memory", u.CPU.String(), u.Memory.String())
			}
			if len(prom.queries) != 2 || prom.queries[0] != tt.wantCPU || prom.queries[1] != tt.wantMemory {
				t.Errorf("queries = %q\nwant [%q %q]", prom.queries, tt.wantCPU, tt.wantMemory)
			}
		})
	}
}

func TestProviderContainerDemandFallsBackToMetricsServer(t *testing.T) {
	broken := httptest.NewServer(&fakePromQL{failOn: func(string) bool { return true }})
	defer broken.Close()
	failing := &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: broken.URL}

	t.Run("VictoriaMetrics failing", func(t *testing.T) {
		ms, _ := metricsServerWith(t,
			podMetric{"shop", "api-1", "api", "150m", "200Mi"},
			podMetric{"shop", "api-1", "proxy", "10m", "20Mi"},
			podMetric{"other", "x", "x", "1", "1Gi"},
		)
		got, demand, err := NewProvider(configClient(t, failing), ms).ContainerDemand(context.Background(), "shop")
		if err != nil {
			t.Fatal(err)
		}
		if demand.Source != SourceMetricsServer || demand.Historical || demand.Window != 0 {
			t.Errorf("demand = %+v, want a current metrics-server reading", demand)
		}
		if demand.Degraded == nil || !strings.Contains(demand.Degraded.Error(), "query container CPU p95") {
			t.Errorf("degraded = %v, want the VictoriaMetrics error", demand.Degraded)
		}
		api, proxy := got[ContainerKey{Pod: "api-1", Container: "api"}], got[ContainerKey{Pod: "api-1", Container: "proxy"}]
		if len(got) != 2 || api.CPU.MilliValue() != 150 || api.Memory.Value() != 200<<20 || proxy.CPU.MilliValue() != 10 {
			t.Errorf("container usage = %v; want each container of the namespace only", got)
		}
	})

	t.Run("VictoriaMetrics disabled", func(t *testing.T) {
		ms, _ := metricsServerWith(t, podMetric{"shop", "api-1", "api", "150m", "200Mi"})
		got, demand, err := NewProvider(configClient(t, nil), ms).ContainerDemand(context.Background(), "shop")
		if err != nil || demand != (Demand{Source: SourceMetricsServer}) || len(got) != 1 {
			t.Errorf("ContainerDemand() = %v, %+v, %v", got, demand, err)
		}
	})

	t.Run("no metrics-server source", func(t *testing.T) {
		got, demand, err := NewProvider(configClient(t, failing), fixedSource{}).ContainerDemand(context.Background(), "shop")
		if !errors.Is(err, ErrUnsupported) || got != nil || demand.Source != SourceMetricsServer || demand.Degraded == nil {
			t.Errorf("ContainerDemand() = %v, %+v, %v; want ErrUnsupported with the VictoriaMetrics error kept", got, demand, err)
		}
	})
}

func TestVMContainerDemandNamesTheFailingQuery(t *testing.T) {
	prom := &fakePromQL{
		answer: func(string) []map[string]any {
			return []map[string]any{sample(map[string]string{"pod": "a", "container": "a"}, "1")}
		},
		failOn: func(q string) bool { return strings.Contains(q, "container_memory_working_set_bytes") },
	}
	srv := httptest.NewServer(prom)
	defer srv.Close()
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ContainerDemand(context.Background(), "shop", time.Hour); err == nil || !strings.Contains(err.Error(), "query container memory peak") {
		t.Errorf("ContainerDemand() = %v, want the memory query named", err)
	}
}
