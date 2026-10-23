package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/rightsizing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// recommendationTTL bounds how often one namespace's demand is recomputed. A two-week p95
// barely moves in minutes, and every dashboard card asks for its namespace.
const recommendationTTL = 5 * time.Minute

type recommendationCache struct {
	mu      sync.Mutex
	entries map[string]cachedReport
}

type cachedReport struct {
	report rightsizing.Report
	at     time.Time
}

func (c *recommendationCache) get(ns string) (rightsizing.Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[ns]
	return e.report, ok && time.Since(e.at) < recommendationTTL
}

func (c *recommendationCache) put(ns string, r rightsizing.Report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cachedReport{}
	}
	c.entries[ns] = cachedReport{report: r, at: time.Now()}
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
	demand, d, err := s.metricsProvider().ContainerDemand(ctx, ns)
	if err != nil {
		if errors.Is(err, metrics.ErrUnsupported) && d.Degraded != nil {
			err = d.Degraded
		}
		return rightsizing.Report{}, fmt.Errorf("no usage data for namespace %s: %w", ns, err)
	}

	report := rightsizing.Analyze(workloads, demand, s.pricingResolver().Rates(ctx), rightsizing.DefaultSettings(d.Historical))
	report.Namespace, report.Source, report.Historical = ns, d.Source, d.Historical
	if d.Historical {
		report.Window = promWindow(d.Window)
		report.Basis = fmt.Sprintf("p95 CPU and peak memory over the last %s from VictoriaMetrics, plus 20%% headroom.", report.Window)
	} else {
		report.Basis = "A single current reading from metrics-server, plus 50% headroom. Connect VictoriaMetrics under " +
			"Settings → Usage metrics for advice based on two weeks of history, which also catches peaks this reading may miss."
	}
	if d.Degraded != nil {
		report.Warning = "VictoriaMetrics is unavailable, so this falls back to metrics-server: " + d.Degraded.Error()
	}
	s.recommendationCache.put(ns, report)
	return report, nil
}

// workloadRequests lists the Deployments, StatefulSets and Argo Rollouts of a namespace
// with the requests of their containers.
func (s *Server) workloadRequests(ctx context.Context, ns string) ([]rightsizing.Workload, error) {
	workloads, err := (&scaling.Engine{Client: s.Client}).Workloads(ctx, ns)
	if err != nil {
		return nil, err
	}
	out := make([]rightsizing.Workload, 0, len(workloads))
	for _, wl := range workloads {
		replicas := wl.Replicas
		out = append(out, toWorkload(wl.Kind, wl.Name, &replicas, wl.Template.Spec))
	}
	return out, nil
}

func toWorkload(kind, name string, replicas *int32, spec corev1.PodSpec) rightsizing.Workload {
	w := rightsizing.Workload{Kind: kind, Name: name, Replicas: 1}
	if replicas != nil {
		w.Replicas = *replicas
	}
	for _, c := range spec.Containers {
		w.Containers = append(w.Containers, rightsizing.Container{
			Name:          c.Name,
			CPURequest:    c.Resources.Requests.Cpu().AsApproximateFloat64(),
			MemoryRequest: c.Resources.Requests.Memory().AsApproximateFloat64(),
		})
	}
	return w
}

// promWindow renders a window the way users read it: "14d", "36h".
func promWindow(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}
