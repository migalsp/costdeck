package metrics

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

// Requests to a namespace are counted from what gateways and meshes export, for idle
// auto-sleep. Each family names the namespace its own way:
//   - Envoy Gateway: envoy_cluster_upstream_rq_total, clusters named
//     httproute/<namespace>/<route>/rule/<n>;
//   - Istio: istio_requests_total from the callers' side (reporter="source"), which stays
//     visible while the namespace's own pods are down;
//   - ingress-nginx: nginx_ingress_controller_requests, by the Ingress's namespace.

// requestFamilies render, for a namespace, a selector per metric family.
var requestFamilies = []func(ns string) string{
	func(ns string) string {
		// %q escapes the regex's backslashes as PromQL strings need.
		return fmt.Sprintf(`envoy_cluster_upstream_rq_total{envoy_cluster_name=~%q`, "httproute/"+regexp.QuoteMeta(ns)+"/.*")
	},
	func(ns string) string {
		return fmt.Sprintf(`istio_requests_total{reporter="source",destination_service_namespace=%q`, ns)
	},
	func(ns string) string { return fmt.Sprintf(`nginx_ingress_controller_requests{namespace=%q`, ns) },
}

// NamespaceRequests sums the requests to a namespace over the window. known is false when
// no family has any series for it: then nothing says whether it is idle.
func (c *VMClient) NamespaceRequests(ctx context.Context, ns string, window time.Duration) (requests float64, known bool, err error) {
	sel := ""
	if c.labelSelector != "" {
		sel = "," + c.labelSelector
	}
	rng := promDuration(window)
	for _, family := range requestFamilies {
		selector := family(ns) + sel + "}"
		n, err := c.scalar(ctx, fmt.Sprintf("count(last_over_time(%s[%s]))", selector, rng))
		if err != nil {
			return 0, false, err
		}
		if n == 0 {
			continue
		}
		known = true
		v, err := c.scalar(ctx, fmt.Sprintf("sum(increase(%s[%s]))", selector, rng))
		if err != nil {
			return 0, false, err
		}
		requests += v
	}
	return requests, known, nil
}

// NamespaceRequests sums the requests to a namespace over the window from VictoriaMetrics;
// ErrNoHistory when VictoriaMetrics is not enabled.
func (p *Provider) NamespaceRequests(ctx context.Context, ns string, window time.Duration) (float64, bool, error) {
	vm, _, err := p.resolve(ctx)
	if vm == nil {
		if err == nil {
			err = ErrNoHistory
		}
		return 0, false, err
	}
	return vm.NamespaceRequests(ctx, ns, window)
}

// NamespaceGPUPeak is the highest utilization, in percent, that any GPU of a namespace
// reached over the window, from DCGM. known is false when DCGM reports no GPU for it. The
// namespace label follows GPUUsage: exported_namespace when a scrape renamed it.
func (c *VMClient) NamespaceGPUPeak(ctx context.Context, ns string, window time.Duration) (peak float64, known bool, err error) {
	sel := ""
	if c.labelSelector != "" {
		sel = "," + c.labelSelector
	}
	labels := []string{"namespace", "pod_namespace"}
	exported, err := c.scalar(ctx, `count(DCGM_FI_DEV_GPU_UTIL{exported_namespace!=""`+sel+`})`)
	if err != nil {
		return 0, false, err
	}
	if exported > 0 {
		labels = []string{"exported_namespace", "pod_namespace"}
	}
	rng := promDuration(window)
	for _, label := range labels {
		selector := fmt.Sprintf(`DCGM_FI_DEV_GPU_UTIL{%s=%q%s}`, label, ns, sel)
		n, err := c.scalar(ctx, fmt.Sprintf("count(last_over_time(%s[%s]))", selector, rng))
		if err != nil {
			return 0, false, err
		}
		if n == 0 {
			continue
		}
		known = true
		v, err := c.scalar(ctx, fmt.Sprintf("max(max_over_time(%s[%s]))", selector, rng))
		if err != nil {
			return 0, false, err
		}
		peak = max(peak, v)
	}
	return peak, known, nil
}

// NamespaceGPUPeak is VMClient.NamespaceGPUPeak through the live settings; ErrNoHistory
// when VictoriaMetrics is not enabled.
func (p *Provider) NamespaceGPUPeak(ctx context.Context, ns string, window time.Duration) (float64, bool, error) {
	vm, _, err := p.resolve(ctx)
	if vm == nil {
		if err == nil {
			err = ErrNoHistory
		}
		return 0, false, err
	}
	return vm.NamespaceGPUPeak(ctx, ns, window)
}
