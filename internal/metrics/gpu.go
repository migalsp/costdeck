package metrics

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// GPU usage comes from NVIDIA's DCGM exporter, scraped into VictoriaMetrics. In Kubernetes
// mode it labels each GPU with the pod using it: namespace, or pod_namespace with
// --use-old-namespace. A scrape that does not honour the exporter's labels (a
// ServiceMonitor's default) keeps its own namespace, the exporter's, and renames the pod's
// to exported_namespace; when that label is there, namespace is not the workload's.

// GPUUsage is what a namespace's GPUs do.
type GPUUsage struct {
	// Utilization is the average share of time the GPUs were busy over the last day, 0–1.
	Utilization float64
	// MemoryUsedGiB is the most GPU memory one GPU of the namespace held over the last
	// week.
	MemoryUsedGiB float64
}

const (
	gpuUtilWindow   = "24h"
	gpuMemoryWindow = "7d"
	gpuCacheTTL     = 2 * time.Minute
)

// GPUUsage returns the GPU usage of every namespace DCGM reports.
func (c *VMClient) GPUUsage(ctx context.Context) (map[string]GPUUsage, error) {
	out := map[string]GPUUsage{}
	labels := []string{"exported_namespace", "pod_namespace"}
	sel := ""
	if c.labelSelector != "" {
		sel = "," + c.labelSelector
	}
	exported, err := c.vector(ctx, `count(DCGM_FI_DEV_GPU_UTIL{exported_namespace!=""`+sel+`})`, "")
	if err != nil {
		return nil, fmt.Errorf("query GPU labels: %w", err)
	}
	if len(exported) == 0 {
		labels = []string{"namespace", "pod_namespace"}
	}
	for _, label := range labels {
		matchers := label + `!=""` + sel
		util, err := c.vector(ctx, fmt.Sprintf("avg by (%s) (avg_over_time(DCGM_FI_DEV_GPU_UTIL{%s}[%s]))", label, matchers, gpuUtilWindow), label)
		if err != nil {
			return nil, fmt.Errorf("query GPU utilization: %w", err)
		}
		mem, err := c.vector(ctx, fmt.Sprintf("max by (%s) (max_over_time(DCGM_FI_DEV_FB_USED{%s}[%s]))", label, matchers, gpuMemoryWindow), label)
		if err != nil {
			return nil, fmt.Errorf("query GPU memory: %w", err)
		}
		for ns, v := range util {
			u := out[ns]
			u.Utilization = min(1, max(0, v/100))
			out[ns] = u
		}
		for ns, v := range mem {
			u := out[ns]
			u.MemoryUsedGiB = v / 1024 // MiB
			out[ns] = u
		}
	}
	return out, nil
}

// gpuCache shares the GPU usage for a short while; every overview request asks for it.
type gpuCache struct {
	mu    sync.Mutex
	key   string
	at    time.Time
	usage map[string]GPUUsage
}

// GPUUsage returns the GPU usage of every namespace from VictoriaMetrics, or nil when
// VictoriaMetrics is not enabled.
func (p *Provider) GPUUsage(ctx context.Context) (map[string]GPUUsage, error) {
	vm, _, err := p.resolve(ctx)
	if vm == nil {
		return nil, err
	}
	p.gpu.mu.Lock()
	defer p.gpu.mu.Unlock()
	key := vm.Endpoint() + "|" + vm.labelSelector
	if p.gpu.key == key && time.Since(p.gpu.at) < gpuCacheTTL {
		return p.gpu.usage, nil
	}
	usage, err := vm.GPUUsage(ctx)
	if err != nil {
		return nil, err
	}
	p.gpu.key, p.gpu.at, p.gpu.usage = key, time.Now(), usage
	return usage, nil
}
