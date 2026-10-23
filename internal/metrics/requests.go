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
