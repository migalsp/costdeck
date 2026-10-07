package metrics

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// ErrNoHistory means VictoriaMetrics is not enabled, so the only usage history is the
// last hour the operator keeps in each NamespaceFinOps status.
var ErrNoHistory = errors.New("usage history beyond the last hour needs VictoriaMetrics")

// historyCacheTTL bounds how often the cluster-wide history is queried: every namespace
// card on the dashboard asks for its slice of it.
const historyCacheTTL = 2 * time.Minute

// historySteps are the resolutions a history can use; HistoryStep picks the finest one
// that keeps a window at roughly a week of hourly points or fewer.
var historySteps = []time.Duration{
	time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// HistoryStep returns the resolution used for a history window: 10 minutes for a day,
// an hour for a week, two hours for two weeks.
func HistoryStep(window time.Duration) time.Duration {
	want := window / 168
	for _, s := range historySteps {
		if s >= want {
			return s
		}
	}
	return historySteps[len(historySteps)-1]
}

// NamespaceHistory returns the usage, requests and limits of every namespace over the
// window ending at end, one point per step. Each point is the average over its step, so
// short spikes are smoothed rather than missed. Requests and limits come from
// kube-state-metrics, counting only pods that are pending or running; they stay empty
// when it is not scraped into VictoriaMetrics.
func (c *VMClient) NamespaceHistory(ctx context.Context, end time.Time, window, step time.Duration) (map[string][]finopsv1.MetricDataPoint, error) {
	avgWindow := promDuration(max(step, 5*time.Minute))
	byNamespace := func(expr string) string { return "sum by (namespace) (" + expr + ")" }
	running := fmt.Sprintf(`on (namespace, pod) group_left () max by (namespace, pod) (%s == 1)`,
		c.series("kube_pod_status_phase", `phase=~"Pending|Running"`))
	allocated := func(metric, res string) string {
		return byNamespace(c.series(metric, `container!=""`, fmt.Sprintf("resource=%q", res)) + " * " + running)
	}
	queries := map[string]string{
		"cpu.usage":    byNamespace(fmt.Sprintf("rate(%s[%s])", c.selector("container_cpu_usage_seconds_total", ""), avgWindow)),
		"mem.usage":    byNamespace(fmt.Sprintf("avg_over_time(%s[%s])", c.selector("container_memory_working_set_bytes", ""), avgWindow)),
		"cpu.requests": allocated("kube_pod_container_resource_requests", "cpu"),
		"cpu.limits":   allocated("kube_pod_container_resource_limits", "cpu"),
		"mem.requests": allocated("kube_pod_container_resource_requests", "memory"),
		"mem.limits":   allocated("kube_pod_container_resource_limits", "memory"),
	}

	type key struct {
		ns string
		ts int64
	}
	values := map[key]map[string]float64{}
	for name, q := range queries {
		series, err := c.queryRange(ctx, q, end.Add(-window), end, step)
		if err != nil {
			return nil, fmt.Errorf("query %s history: %w", name, err)
		}
		for ns, samples := range series {
			for ts, v := range samples {
				k := key{ns, ts}
				if values[k] == nil {
					values[k] = map[string]float64{}
				}
				values[k][name] = v
			}
		}
	}

	// A namespace scaled to zero has no container series at all, and one whose pods just
	// started has no CPU rate yet, so VictoriaMetrics leaves those steps or values out.
	// For a namespace it knows, every step of the window gets a point, and what is missing
	// means nothing ran: zero. Requests and limits are only zeroed when kube-state-metrics
	// is there at all; otherwise they stay empty, for the caller to fill in.
	allocation := false
	for _, v := range values {
		_, has := v["cpu.requests"]
		allocation = allocation || has
	}
	defaults := []string{"cpu.usage", "mem.usage"}
	if allocation {
		defaults = append(defaults, "cpu.requests", "cpu.limits", "mem.requests", "mem.limits")
	}

	out := map[string][]finopsv1.MetricDataPoint{}
	for k := range values {
		out[k.ns] = nil
	}
	stepSeconds := int64(step.Seconds())
	for ns := range out {
		for ts := end.Add(-window).Unix(); ts <= end.Unix(); ts += stepSeconds {
			v := map[string]float64{}
			maps.Copy(v, values[key{ns, ts}])
			for _, d := range defaults {
				if _, ok := v[d]; !ok {
					v[d] = 0
				}
			}
			out[ns] = append(out[ns], finopsv1.MetricDataPoint{
				Timestamp: metav1.NewTime(time.Unix(ts, 0)),
				CPU:       finopsv1.ResourceMetrics{Usage: cpuValue(v, "cpu.usage"), Requests: cpuValue(v, "cpu.requests"), Limits: cpuValue(v, "cpu.limits")},
				Memory:    finopsv1.ResourceMetrics{Usage: memoryValue(v, "mem.usage"), Requests: memoryValue(v, "mem.requests"), Limits: memoryValue(v, "mem.limits")},
			})
		}
	}
	return out, nil
}

// series renders a selector with the configured label selector but, unlike selector,
// without the container matchers, which series such as kube_pod_status_phase lack.
func (c *VMClient) series(metric string, matchers ...string) string {
	if c.labelSelector != "" {
		matchers = append(matchers, c.labelSelector)
	}
	return metric + "{" + strings.Join(matchers, ",") + "}"
}

func cpuValue(v map[string]float64, name string) string {
	x, ok := v[name]
	if !ok {
		return ""
	}
	return resource.NewMilliQuantity(int64(x*1000+0.5), resource.DecimalSI).String()
}

func memoryValue(v map[string]float64, name string) string {
	x, ok := v[name]
	if !ok {
		return ""
	}
	return resource.NewQuantity(int64(x), resource.BinarySI).String()
}

// promRangeResponse is the Prometheus HTTP API range query response.
type promRangeResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType,omitempty"`
	Error     string `json:"error,omitempty"`
	Data      struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"` // [[timestamp, "value"], ...]
		} `json:"result"`
	} `json:"data"`
}

// queryRange runs a range query grouped by namespace and returns each namespace's samples
// keyed by Unix time.
func (c *VMClient) queryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (map[string]map[int64]float64, error) {
	var pr promRangeResponse
	err := c.get(ctx, "/api/v1/query_range", url.Values{
		"query": {query},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatInt(int64(step.Seconds()), 10)},
	}, &pr)
	if err != nil {
		return nil, err
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("query failed: %s: %s", pr.ErrorType, pr.Error)
	}
	out := make(map[string]map[int64]float64, len(pr.Data.Result))
	for _, r := range pr.Data.Result {
		ns := r.Metric["namespace"]
		if ns == "" {
			continue
		}
		samples := make(map[int64]float64, len(r.Values))
		for _, s := range r.Values {
			ts, ok := s[0].(float64)
			if !ok {
				return nil, fmt.Errorf("unexpected timestamp type %T", s[0])
			}
			v, err := sampleValue(s)
			if err != nil {
				return nil, err
			}
			samples[int64(ts)] = v
		}
		out[ns] = samples
	}
	return out, nil
}
