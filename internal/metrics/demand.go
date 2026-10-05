package metrics

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// maxDemandWindow caps the history a right-sizing query scans. Two weeks covers weekly
// cycles; scanning a 30-day retention would cost far more for little extra signal.
const maxDemandWindow = 14 * 24 * time.Hour

// ContainerKey identifies one container of one pod.
type ContainerKey struct {
	Pod       string
	Container string
}

// Demand describes how container demand was measured.
type Demand struct {
	Source string
	// Historical is true when the figures are a p95 (CPU) and a peak (memory) over Window,
	// false when they are a single current reading.
	Historical bool
	Window     time.Duration
	// Degraded is the VictoriaMetrics error that forced a fallback to metrics-server.
	Degraded error
}

// ContainerDemand returns per-container demand in a namespace: p95 CPU and peak memory
// over the lookback window from VictoriaMetrics, or the current reading from
// metrics-server when no history is available.
func (p *Provider) ContainerDemand(ctx context.Context, namespace string) (map[ContainerKey]Usage, Demand, error) {
	vm, lookback, vmErr := p.resolve(ctx)
	if vm != nil {
		window := min(lookback, maxDemandWindow)
		u, err := vm.ContainerDemand(ctx, namespace, window)
		if err == nil {
			return u, Demand{Source: SourceVictoriaMetrics, Historical: true, Window: window}, nil
		}
		vmErr = err
	}
	ms, ok := p.MetricsServer.(*MetricsServerSource)
	if !ok {
		return nil, Demand{Source: SourceMetricsServer, Degraded: vmErr}, ErrUnsupported
	}
	u, err := ms.ContainerUsage(ctx, namespace)
	return u, Demand{Source: SourceMetricsServer, Degraded: vmErr}, err
}

// ContainerDemand returns the p95 CPU and the peak working-set memory of every container
// in a namespace over the window.
func (c *VMClient) ContainerDemand(ctx context.Context, namespace string, window time.Duration) (map[ContainerKey]Usage, error) {
	rng := promDuration(window)
	cpu, err := c.containerVector(ctx, fmt.Sprintf(
		"quantile_over_time(0.95, sum by (pod, container) (%s)[%s:5m])", c.cpuExpr(namespace), rng))
	if err != nil {
		return nil, fmt.Errorf("query container CPU p95: %w", err)
	}
	mem, err := c.containerVector(ctx, fmt.Sprintf(
		"max_over_time(max by (pod, container) (%s)[%s:5m])", c.memExpr(namespace), rng))
	if err != nil {
		return nil, fmt.Errorf("query container memory peak: %w", err)
	}
	return mergeUsage(cpu, mem), nil
}

// containerVector runs an instant query whose series carry pod and container labels.
func (c *VMClient) containerVector(ctx context.Context, query string) (map[ContainerKey]float64, error) {
	resp, err := c.query(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make(map[ContainerKey]float64, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		v, err := sampleValue(r.Value)
		if err != nil {
			return nil, err
		}
		out[ContainerKey{Pod: r.Metric["pod"], Container: r.Metric["container"]}] = v
	}
	return out, nil
}

// ContainerUsage returns the current usage of every container in a namespace.
func (s *MetricsServerSource) ContainerUsage(ctx context.Context, namespace string) (map[ContainerKey]Usage, error) {
	if s.Client == nil {
		return nil, fmt.Errorf("metrics API client is not configured")
	}
	list, err := s.Client.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pod metrics from metrics-server: %w", err)
	}
	out := make(map[ContainerKey]Usage)
	for _, pm := range list.Items {
		for _, c := range pm.Containers {
			out[ContainerKey{Pod: pm.Name, Container: c.Name}] = Usage{CPU: *c.Usage.Cpu(), Memory: *c.Usage.Memory()}
		}
	}
	return out, nil
}

func mergeUsage(cpu, mem map[ContainerKey]float64) map[ContainerKey]Usage {
	out := make(map[ContainerKey]Usage, len(cpu))
	for k, v := range cpu {
		out[k] = newUsage(v, mem[k])
	}
	for k, v := range mem {
		if _, ok := out[k]; !ok {
			out[k] = newUsage(0, v)
		}
	}
	return out
}
