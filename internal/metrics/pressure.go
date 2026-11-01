package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type ContainerPressure struct {
	ThrottledShare float64
	OOMKilled      bool
	Restarts       float64
}

type PressureHistory struct {
	Containers  map[ContainerKey]ContainerPressure
	EvictedPods map[string]bool
	Window      time.Duration
}

func (p *Provider) PressureHistory(ctx context.Context, namespace string) (*PressureHistory, error) {
	vm, lookback, err := p.resolve(ctx)
	if vm == nil {
		return nil, err
	}
	return vm.PressureHistory(ctx, namespace, min(lookback, maxDemandWindow))
}

func (c *VMClient) PressureHistory(ctx context.Context, namespace string, window time.Duration) (*PressureHistory, error) {
	rng := promDuration(window)
	throttled, err := c.containerVector(ctx, fmt.Sprintf(
		"quantile_over_time(0.95, (sum by (pod, container) (rate(%s[5m])) / sum by (pod, container) (rate(%s[5m])))[%s:5m])",
		c.selector("container_cpu_cfs_throttled_periods_total", namespace), c.selector("container_cpu_cfs_periods_total", namespace), rng))
	if err != nil {
		return nil, fmt.Errorf("query CPU throttling: %w", err)
	}
	oom, err := c.containerVector(ctx, fmt.Sprintf("max by (pod, container) (max_over_time(%s[%s]))",
		c.selectorWith("kube_pod_container_status_last_terminated_reason", namespace, true, `reason="OOMKilled"`), rng))
	if err != nil {
		return nil, fmt.Errorf("query OOM kills: %w", err)
	}
	restarts, err := c.containerVector(ctx, fmt.Sprintf("max by (pod, container) (increase(%s[%s]))",
		c.selector("kube_pod_container_status_restarts_total", namespace), rng))
	if err != nil {
		return nil, fmt.Errorf("query restarts: %w", err)
	}
	evicted, err := c.vector(ctx, fmt.Sprintf("max by (pod) (max_over_time(%s[%s])) > 0",
		c.selectorWith("kube_pod_status_reason", namespace, false, `reason="Evicted"`), rng), "pod")
	if err != nil {
		return nil, fmt.Errorf("query evictions: %w", err)
	}
	h := &PressureHistory{Containers: map[ContainerKey]ContainerPressure{}, EvictedPods: map[string]bool{}, Window: window}
	for k, v := range throttled {
		p := h.Containers[k]
		p.ThrottledShare = v
		h.Containers[k] = p
	}
	for k, v := range restarts {
		p := h.Containers[k]
		p.Restarts = v
		p.OOMKilled = oom[k] > 0 && v > 0
		h.Containers[k] = p
	}
	for pod := range evicted {
		h.EvictedPods[pod] = true
	}
	return h, nil
}

func (c *VMClient) selectorWith(metric, namespace string, containerLevel bool, extra ...string) string {
	matchers := []string{fmt.Sprintf("namespace=%q", namespace)}
	if containerLevel {
		matchers = append(matchers, `container!=""`, `container!="POD"`)
	}
	matchers = append(matchers, extra...)
	if c.labelSelector != "" {
		matchers = append(matchers, c.labelSelector)
	}
	return metric + "{" + strings.Join(matchers, ",") + "}"
}
