package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/jvm"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/rightsizing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// +kubebuilder:rbac:groups=finops.costdeck.io,resources=rightsizingpolicies,verbs=get;list

const policiesUnreadable = "right-sizing policies could not be read"

// recommendationTTL bounds how often one namespace's demand is recomputed. A two-week p95
// barely moves in minutes, and every dashboard card asks for its namespace.
const recommendationTTL = 5 * time.Minute

// partialTTL is how long advice is kept while some containers have no usage yet, such as
// the pods of a namespace that just started: their data comes within a minute or two.
const partialTTL = 30 * time.Second

type recommendationCache struct {
	mu      sync.Mutex
	entries map[string]cachedReport
}

type cachedReport struct {
	report rightsizing.Report
	at     time.Time
	ttl    time.Duration
}

func (c *recommendationCache) get(ns string) (rightsizing.Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[ns]
	return e.report, ok && time.Since(e.at) < e.ttl
}

func (c *recommendationCache) put(ns string, r rightsizing.Report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cachedReport{}
	}
	for k, e := range c.entries {
		if time.Since(e.at) >= e.ttl {
			delete(c.entries, k)
		}
	}
	ttl := recommendationTTL
	for _, w := range r.Workloads {
		for _, ca := range w.Containers {
			if ca.CPU.Action == rightsizing.ActionUnknown || ca.Memory.Action == rightsizing.ActionUnknown {
				ttl = partialTTL
			}
		}
	}
	c.entries[ns] = cachedReport{report: r, at: time.Now(), ttl: ttl}
}

// drop removes the advice of a workload just changed and keeps the rest, which still holds:
// working everything out again while the workload rolls out would ask for usage the new
// pods do not have yet.
func (c *recommendationCache) drop(ns, kind, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[ns]
	if !ok {
		return
	}
	r := e.report
	kept := make([]rightsizing.WorkloadAdvice, 0, len(r.Workloads))
	r.MonthlySavings, r.JavaMonthlySavings = 0, 0
	for _, w := range r.Workloads {
		if w.Kind == kind && w.Name == name {
			continue
		}
		kept = append(kept, w)
		r.MonthlySavings += w.MonthlySavings
		for _, c := range w.Containers {
			if c.JVM != nil {
				r.JavaMonthlySavings += c.JVM.MonthlySavings
			}
		}
	}
	r.Workloads = kept
	e.report = r
	c.entries[ns] = e
}

// forget drops a namespace's advice, which a change of its requests makes stale.
func (c *recommendationCache) forget(ns string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, ns)
}

// handleRecommendations serves read-only right-sizing advice for a namespace.
func (s *Server) handleRecommendations(w http.ResponseWriter, r *http.Request) {
	report, err := s.recommendations(r.Context(), r.PathValue("ns"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// recommendations compares a namespace's requests with observed demand.
func (s *Server) recommendations(ctx context.Context, ns string) (rightsizing.Report, error) {
	if report, ok := s.recommendationCache.get(ns); ok {
		return report, nil
	}
	workloads, err := s.workloadRequests(ctx, ns)
	if err != nil {
		return rightsizing.Report{}, err
	}
	if len(workloads) == 0 {
		return rightsizing.Report{Namespace: ns, Workloads: []rightsizing.WorkloadAdvice{}}, nil
	}
	demand, d, err := s.metricsProvider().ContainerDemand(ctx, ns)
	if err != nil {
		if errors.Is(err, metrics.ErrUnsupported) && d.Degraded != nil {
			err = d.Degraded
		}
		return rightsizing.Report{}, fmt.Errorf("no usage data for namespace %s: %w", ns, err)
	}

	holdUnderPressure(workloads, s.pressure(ctx, ns, workloads), demand)
	heapDemand, heapErr := s.metricsProvider().HeapDemand(ctx, ns)
	report := rightsizing.Analyze(workloads, demand, heapDemand, s.pricingResolver().Rates(ctx), rightsizing.DefaultSettings(d.Historical))
	report.Namespace, report.Source, report.Historical = ns, d.Source, d.Historical
	if d.Historical {
		report.Window = promWindow(d.Window)
		report.Basis = fmt.Sprintf("p95 CPU and peak memory over the last %s from VictoriaMetrics, plus 20%% headroom.", report.Window)
	} else {
		report.Basis = "A single current reading from metrics-server, plus 50% headroom. Connect VictoriaMetrics under " +
			"Settings → Usage metrics for advice based on two weeks of history, which also catches peaks this reading may miss."
	}
	if hasJavaAdvice(report) {
		report.Basis += " Java containers: the memory request covers the heap the JVM can grow to plus native memory; " +
			"heap advice is twice the heap left after garbage collection, from JVM metrics in VictoriaMetrics."
	}
	switch {
	case d.Degraded != nil:
		report.Warning = "VictoriaMetrics is unavailable, so this falls back to metrics-server: " + d.Degraded.Error()
	case heapErr != nil:
		report.Warning = "JVM heap metrics could not be read, so Java advice rests on the JVM flags alone: " + heapErr.Error()
	}
	if !slices.ContainsFunc(workloads, func(w rightsizing.Workload) bool { return w.Policy != nil && w.Policy.Name == policiesUnreadable }) {
		s.recommendationCache.put(ns, report)
	}
	return report, nil
}

// workloadRequests lists the Deployments, StatefulSets and Argo Rollouts of a namespace
// with the requests of their containers.
func (s *Server) workloadRequests(ctx context.Context, ns string) ([]rightsizing.Workload, error) {
	workloads, err := (&scaling.Engine{Client: s.Client}).Workloads(ctx, ns)
	if err != nil {
		return nil, err
	}
	var policies finopsv1.RightsizingPolicyList
	policiesRead := true
	if err := s.Client.List(ctx, &policies, client.InNamespace(ns)); err != nil && !meta.IsNoMatchError(err) {
		policiesRead = false
		logf.FromContext(ctx).Error(err, "Could not read right-sizing policies; advice cannot be applied until they can be", "namespace", ns)
	}
	slices.SortStableFunc(policies.Items, func(a, b finopsv1.RightsizingPolicy) int {
		return cmp.Or(a.CreationTimestamp.Compare(b.CreationTimestamp.Time), cmp.Compare(a.Name, b.Name))
	})
	out := make([]rightsizing.Workload, 0, len(workloads))
	for _, wl := range workloads {
		policy := policyFor(policies.Items, wl.Template.Labels)
		if policy != nil && policy.Spec.Exclude {
			continue
		}
		replicas := wl.Replicas
		runtimeOverride := cmp.Or(wl.Annotations[jvm.RuntimeAnnotation], wl.Template.Annotations[jvm.RuntimeAnnotation])
		w := toWorkload(wl.Kind, wl.Name, &replicas, wl.Template.Spec, runtimeOverride)
		switch {
		case !policiesRead:
			w.Policy = &rightsizing.Policy{Name: policiesUnreadable, ApplyForbidden: true}
		case policy != nil:
			w.Policy = toPolicy(*policy)
		}
		out = append(out, w)
	}
	return out, nil
}

func policyFor(policies []finopsv1.RightsizingPolicy, podLabels map[string]string) *finopsv1.RightsizingPolicy {
	for i := range policies {
		sel := policies[i].Spec.Selector
		if sel == nil {
			return &policies[i]
		}
		selector, err := metav1.LabelSelectorAsSelector(sel)
		if err == nil && selector.Matches(labels.Set(podLabels)) {
			return &policies[i]
		}
	}
	return nil
}

func toPolicy(p finopsv1.RightsizingPolicy) *rightsizing.Policy {
	bounds := func(r finopsv1.ResourcePolicy) rightsizing.Bounds {
		var b rightsizing.Bounds
		if r.Min != nil {
			b.Min = r.Min.AsApproximateFloat64()
		}
		if r.Max != nil {
			b.Max = r.Max.AsApproximateFloat64()
		}
		if r.HeadroomPercent != nil {
			multiplier := 1 + float64(*r.HeadroomPercent)/100
			b.Headroom = &multiplier
		}
		return b
	}
	return &rightsizing.Policy{
		Name: p.Name, ApplyForbidden: p.Spec.Apply == finopsv1.RightsizingApplyForbidden,
		CPU: bounds(p.Spec.CPU), Memory: bounds(p.Spec.Memory),
	}
}

func toWorkload(kind, name string, replicas *int32, spec corev1.PodSpec, runtimeOverride string) rightsizing.Workload {
	w := rightsizing.Workload{Kind: kind, Name: name, Replicas: 1}
	if replicas != nil {
		w.Replicas = *replicas
	}
	for _, c := range spec.Containers {
		container := rightsizing.Container{
			Name:          c.Name,
			CPURequest:    c.Resources.Requests.Cpu().AsApproximateFloat64(),
			MemoryRequest: c.Resources.Requests.Memory().AsApproximateFloat64(),
			MemoryLimit:   c.Resources.Limits.Memory().AsApproximateFloat64(),
			CPULimit:      c.Resources.Limits.Cpu().AsApproximateFloat64(),
		}
		if java, ok := jvm.Inspect(c, runtimeOverride); ok {
			container.Java = &java
		}
		w.Containers = append(w.Containers, container)
	}
	return w
}

func hasJavaAdvice(r rightsizing.Report) bool {
	return slices.ContainsFunc(r.Workloads, func(w rightsizing.WorkloadAdvice) bool {
		return slices.ContainsFunc(w.Containers, func(c rightsizing.ContainerAdvice) bool { return c.JVM != nil })
	})
}

// promWindow renders a window the way users read it: "14d", "36h".
func promWindow(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}
