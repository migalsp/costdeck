package metrics

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func TestHeapDemandReadsEveryJVMNaming(t *testing.T) {
	orders := map[string]string{"pod": "orders-7d9f8-abcde", "container": "app"}
	prom := &fakePromQL{answer: func(q string) []map[string]any {
		switch {
		case strings.HasPrefix(q, "max_over_time(min_over_time("):
			return []map[string]any{sample(orders, "419430400")}
		case strings.HasPrefix(q, "max_over_time(") && strings.Contains(q, `area="nonheap"`):
			return []map[string]any{sample(orders, "157286400")}
		case strings.HasPrefix(q, "max_over_time("):
			return []map[string]any{sample(orders, "1610612736"), sample(map[string]string{"pod": "billing-0"}, "268435456")}
		}
		return nil
	}}
	srv := httptest.NewServer(prom)
	defer srv.Close()
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL, LabelSelector: `cluster="eu"`})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.HeapDemand(context.Background(), "shop", 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if u := got[ContainerKey{Pod: "orders-7d9f8-abcde", Container: "app"}]; u.Used != 1610612736 || u.AfterGC != 419430400 || u.NonHeap != 157286400 {
		t.Errorf("orders heap = %+v", u)
	}
	if u := got[ContainerKey{Pod: "billing-0"}]; u.Used != 268435456 {
		t.Errorf("a JVM whose series carry no container label must still count, got %+v", u)
	}
	if len(prom.queries) != 3 {
		t.Fatalf("queries = %d, want heap peak, heap after GC and non-heap", len(prom.queries))
	}
	for _, q := range prom.queries {
		for _, want := range []string{`jvm_memory_used_bytes{namespace="shop",area=`, `jvm_memory_bytes_used{namespace="shop",area=`, `jvm_memory_type=`, `cluster="eu"`, "[14d:5m]"} {
			if !strings.Contains(q, want) {
				t.Errorf("query %q lacks %s", q, want)
			}
		}
	}
	if !strings.Contains(prom.queries[1], "min_over_time(") || !strings.Contains(prom.queries[1], "[15m:1m]") {
		t.Errorf("the heap after GC is the peak of 15-minute minimums, got %q", prom.queries[1])
	}
}

func TestHeapDemandStopsWithoutJVMSeries(t *testing.T) {
	prom := &fakePromQL{answer: func(string) []map[string]any { return nil }}
	srv := httptest.NewServer(prom)
	defer srv.Close()
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.HeapDemand(context.Background(), "shop", 24*time.Hour)
	if err != nil || got != nil || len(prom.queries) != 1 {
		t.Errorf("got %v, %v after %d queries; a namespace without JVM series needs one query and no data", got, err, len(prom.queries))
	}
}

func TestProviderHeapDemand(t *testing.T) {
	orders := map[string]string{"pod": "orders-0", "container": "app"}
	prom := &fakePromQL{answer: func(q string) []map[string]any {
		switch {
		case strings.HasPrefix(q, "max_over_time(min_over_time("):
			return []map[string]any{sample(orders, "100")}
		case strings.Contains(q, `area="nonheap"`):
			return []map[string]any{sample(orders, "50")}
		}
		return []map[string]any{sample(orders, "400")}
	}}
	srv := httptest.NewServer(prom)
	defer srv.Close()

	t.Run("disabled", func(t *testing.T) {
		got, err := NewProvider(configClient(t, nil), fixedSource{}).HeapDemand(context.Background(), "shop")
		if got != nil || err != nil {
			t.Errorf("HeapDemand() = %v, %v; want no data and no error", got, err)
		}
	})
	t.Run("misconfigured", func(t *testing.T) {
		got, err := NewProvider(configClient(t, &finopsv1.VictoriaMetricsConfig{Enabled: true}), fixedSource{}).HeapDemand(context.Background(), "shop")
		if got != nil || err == nil || !strings.Contains(err.Error(), "no endpoint") {
			t.Errorf("HeapDemand() = %v, %v; want the configuration error", got, err)
		}
	})
	if len(prom.queries) != 0 {
		t.Fatalf("queries without VictoriaMetrics: %v", prom.queries)
	}
	t.Run("enabled", func(t *testing.T) {
		vm := &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: srv.URL, RetentionDays: 30}
		got, err := NewProvider(configClient(t, vm), fixedSource{}).HeapDemand(context.Background(), "shop")
		if err != nil {
			t.Fatal(err)
		}
		if u := got[ContainerKey{Pod: "orders-0", Container: "app"}]; u != (HeapUsage{Used: 400, AfterGC: 100, NonHeap: 50}) {
			t.Errorf("orders heap = %+v", u)
		}
		for _, q := range prom.queries {
			if !strings.HasSuffix(q, "[14d:5m])") {
				t.Errorf("query %q must scan the lookback capped at two weeks", q)
			}
		}
	})
}

func TestHeapDemandNamesTheFailingQuery(t *testing.T) {
	isAfterGC := func(q string) bool { return strings.HasPrefix(q, "max_over_time(min_over_time(") }
	isNonHeap := func(q string) bool { return strings.Contains(q, `area="nonheap"`) }
	tests := []struct {
		name   string
		failOn func(string) bool
		want   string
	}{
		{"heap peak", func(q string) bool { return !isAfterGC(q) && !isNonHeap(q) }, "query JVM heap peak"},
		{"heap after GC", isAfterGC, "query JVM heap after garbage collection"},
		{"non-heap", isNonHeap, "query JVM non-heap peak"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prom := &fakePromQL{
				answer: func(string) []map[string]any {
					return []map[string]any{sample(map[string]string{"pod": "p", "container": "c"}, "1")}
				},
				failOn: tt.failOn,
			}
			srv := httptest.NewServer(prom)
			defer srv.Close()
			c, err := NewVMClient(VMOptions{Endpoint: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.HeapDemand(context.Background(), "shop", time.Hour)
			if got != nil || err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "HTTP 500") {
				t.Errorf("HeapDemand() = %v, %v; want an error naming %q", got, err, tt.want)
			}
		})
	}
}
