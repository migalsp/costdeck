package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type HeapUsage struct {
	Used    float64
	AfterGC float64
	NonHeap float64
}

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

const heapAfterGCWindow = "15m:1m"

func (p *Provider) HeapDemand(ctx context.Context, namespace string) (map[ContainerKey]HeapUsage, error) {
	vm, lookback, err := p.resolve(ctx)
	if vm == nil {
		return nil, err
	}
	return vm.HeapDemand(ctx, namespace, min(lookback, maxDemandWindow))
}

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
	afterGC, err := c.containerVector(ctx, fmt.Sprintf("max_over_time(min_over_time((%s)[%s])[%s:5m])", heap, heapAfterGCWindow, rng))
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
