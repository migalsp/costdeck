package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// HeapUsage is the JVM memory of one container over the window, in bytes: the peak heap in use,
// the peak heap left after garbage collection and the peak non-heap memory. AfterGC is zero when
// no 15 minutes of the window had enough samples to tell.
type HeapUsage struct {
	Used    float64
	AfterGC float64
	NonHeap float64
}

// jvmMemorySeries is one exporter's naming of JVM memory in use: the metric and the label matchers
// that pick its heap and its non-heap areas.
type jvmMemorySeries struct {
	metric  string
	heap    string
	nonHeap string
}

var jvmMemoryNamings = []jvmMemorySeries{
	{metric: "jvm_memory_used_bytes", heap: `area="heap"`, nonHeap: `area="nonheap"`},
	{metric: "jvm_memory_bytes_used", heap: `area="heap"`, nonHeap: `area="nonheap"`},
	{metric: "jvm_memory_used_bytes", heap: `jvm_memory_type="heap"`, nonHeap: `jvm_memory_type="non_heap"`},
}

const (
	heapAfterGCWindow     = "15m:1m"
	heapAfterGCMinSamples = 10
)

// HeapDemand returns the JVM memory of the containers in a namespace over the lookback window,
// capped at maxDemandWindow. It needs VictoriaMetrics: without it the result is nil, with an error
// only when VictoriaMetrics is enabled but unusable.
func (p *Provider) HeapDemand(ctx context.Context, namespace string) (map[ContainerKey]HeapUsage, error) {
	vm, lookback, err := p.resolve(ctx)
	if vm == nil {
		return nil, err
	}
	return vm.HeapDemand(ctx, namespace, min(lookback, maxDemandWindow))
}

// HeapDemand queries the heap, heap-after-GC and non-heap peaks per container at 5-minute steps.
// The heap after GC is the highest of the 15-minute minimums that have at least 10 samples.
// Containers without heap series are left out; nil means no container exports them.
func (c *VMClient) HeapDemand(ctx context.Context, namespace string, window time.Duration) (map[ContainerKey]HeapUsage, error) {
	rng := promDuration(window)
	heap, nonHeap := c.jvmMemoryExpr(namespace, true), c.jvmMemoryExpr(namespace, false)
	used, err := c.containerVector(ctx, fmt.Sprintf("max_over_time((%s)[%s:5m])", heap, rng))
	if err != nil {
		return nil, fmt.Errorf("query JVM heap peak: %w", err)
	}
	if len(used) == 0 {
		return nil, nil
	}
	afterGC, err := c.containerVector(ctx, fmt.Sprintf("max_over_time((min_over_time((%[1]s)[%[2]s]) and count_over_time((%[1]s)[%[2]s]) >= %[3]d)[%[4]s:5m])",
		heap, heapAfterGCWindow, heapAfterGCMinSamples, rng))
	if err != nil {
		return nil, fmt.Errorf("query JVM heap after garbage collection: %w", err)
	}
	native, err := c.containerVector(ctx, fmt.Sprintf("max_over_time((%s)[%s:5m])", nonHeap, rng))
	if err != nil {
		return nil, fmt.Errorf("query JVM non-heap peak: %w", err)
	}
	out := make(map[ContainerKey]HeapUsage, len(used))
	for k, v := range used {
		out[k] = HeapUsage{Used: v, AfterGC: afterGC[k], NonHeap: native[k]}
	}
	return out, nil
}

// jvmMemoryExpr sums the heap or non-heap memory in use per container for every known naming,
// joined with "or" so whichever one an application exports is found; the first naming wins when a
// container exports several.
func (c *VMClient) jvmMemoryExpr(namespace string, heap bool) string {
	parts := make([]string, 0, len(jvmMemoryNamings))
	for _, n := range jvmMemoryNamings {
		area := n.nonHeap
		if heap {
			area = n.heap
		}
		matchers := []string{fmt.Sprintf("namespace=%q", namespace), area}
		if c.labelSelector != "" {
			matchers = append(matchers, c.labelSelector)
		}
		parts = append(parts, fmt.Sprintf("sum by (namespace, pod, container) (%s{%s})", n.metric, strings.Join(matchers, ",")))
	}
	return strings.Join(parts, " or ")
}
