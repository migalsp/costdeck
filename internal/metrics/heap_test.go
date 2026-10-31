package metrics

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
