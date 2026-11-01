package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sync"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/rightsizing"
	"github.com/migalsp/costdeck-operator/internal/risks"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list

const (
	kindScalingGroup  = "ScalingGroup"
	kindScalingConfig = "ScalingConfig"
)

type containerRef struct {
	workloadRef
	container string
}

type workloadPressure struct {
	evictedPods     map[string]bool
	evictionMessage string
	hpa             *risks.HPA
}

type observations struct {
	containers map[containerRef]risks.Pressure
	workloads  map[workloadRef]*workloadPressure
	historical bool
	window     time.Duration
	warnings   []string
}

func (o observations) workload(ref workloadRef) *workloadPressure {
	if wp, ok := o.workloads[ref]; ok {
		return wp
	}
	wp := &workloadPressure{evictedPods: map[string]bool{}}
	o.workloads[ref] = wp
	return wp
}

type pressureCache struct {
	mu      sync.Mutex
	entries map[string]cachedObservations
}

type cachedObservations struct {
	obs observations
	at  time.Time
}

func (c *pressureCache) get(ns string) (observations, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[ns]
	return e.obs, ok && time.Since(e.at) < recommendationTTL
}

func (c *pressureCache) put(ns string, obs observations) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cachedObservations{}
	}
	for k, e := range c.entries {
		if time.Since(e.at) >= recommendationTTL {
			delete(c.entries, k)
		}
	}
	c.entries[ns] = cachedObservations{obs: obs, at: time.Now()}
}

func (s *Server) pressure(ctx context.Context, ns string, workloads []rightsizing.Workload) observations {
	if obs, ok := s.pressureCache.get(ns); ok {
		return obs
	}
	obs := observations{containers: map[containerRef]risks.Pressure{}, workloads: map[workloadRef]*workloadPressure{}}
	owner := func(pod string) (workloadRef, bool) {
		var best workloadRef
		for _, w := range workloads {
			if rightsizing.OwnsPod(w.Kind, w.Name, pod) && len(w.Name) > len(best.Name) {
				best = workloadRef{w.Kind, w.Name}
			}
		}
		return best, best.Name != ""
	}
	var pods corev1.PodList
	if err := s.Client.List(ctx, &pods, client.InNamespace(ns)); err != nil {
		obs.warnings = append(obs.warnings, "pods could not be read: "+err.Error())
	}
	for _, pod := range pods.Items {
		ref, ok := owner(pod.Name)
		if !ok {
			continue
		}
		if pod.Status.Phase == corev1.PodFailed && pod.Status.Reason == "Evicted" {
			wp := obs.workload(ref)
			wp.evictedPods[pod.Name] = true
			wp.evictionMessage = pod.Status.Message
			continue
		}
		for _, cs := range pod.Status.ContainerStatuses {
			key := containerRef{ref, cs.Name}
			p := obs.containers[key]
			p.Restarts = max(p.Restarts, int(cs.RestartCount))
			for _, t := range []*corev1.ContainerStateTerminated{cs.LastTerminationState.Terminated, cs.State.Terminated} {
				if t == nil {
					continue
				}
				p.LastReason, p.LastExitCode = t.Reason, t.ExitCode
				if t.Reason == risks.ReasonOOMKilled {
					p.OOMKills = max(p.OOMKills, 1)
				}
			}
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
				p.CrashLooping = true
			}
			obs.containers[key] = p
		}
	}
	var hpas autoscalingv2.HorizontalPodAutoscalerList
	if err := s.Client.List(ctx, &hpas, client.InNamespace(ns)); err != nil {
		obs.warnings = append(obs.warnings, "HorizontalPodAutoscalers could not be read: "+err.Error())
	}
	for _, h := range hpas.Items {
		limited := false
		for _, c := range h.Status.Conditions {
			if c.Type == autoscalingv2.ScalingLimited && c.Status == corev1.ConditionTrue && c.Reason == "TooManyReplicas" {
				limited = true
			}
		}
		ref := workloadRef{h.Spec.ScaleTargetRef.Kind, h.Spec.ScaleTargetRef.Name}
		obs.workload(ref).hpa = &risks.HPA{Name: h.Name, Max: h.Spec.MaxReplicas, Wanted: wantedReplicas(h), Limited: limited}
	}
	history, err := s.metricsProvider().PressureHistory(ctx, ns)
	if err != nil {
		obs.warnings = append(obs.warnings, "VictoriaMetrics could not be read for CPU throttling and past OOM kills, restarts and evictions: "+err.Error())
	}
	if history != nil {
		obs.historical, obs.window = true, history.Window
		mergeHistory(obs, history, owner)
	}
	s.pressureCache.put(ns, obs)
	return obs
}

func wantedReplicas(h autoscalingv2.HorizontalPodAutoscaler) int32 {
	targets := map[corev1.ResourceName]int32{}
	for _, m := range h.Spec.Metrics {
		if m.Type == autoscalingv2.ResourceMetricSourceType && m.Resource != nil && m.Resource.Target.AverageUtilization != nil {
			targets[m.Resource.Name] = *m.Resource.Target.AverageUtilization
		}
	}
	wanted := int32(0)
	for _, m := range h.Status.CurrentMetrics {
		if m.Type != autoscalingv2.ResourceMetricSourceType || m.Resource == nil || m.Resource.Current.AverageUtilization == nil {
			continue
		}
		target := targets[m.Resource.Name]
		if target <= 0 {
			continue
		}
		current := float64(*m.Resource.Current.AverageUtilization)
		wanted = max(wanted, int32(math.Ceil(float64(h.Status.CurrentReplicas)*current/float64(target))))
	}
	return wanted
}

func mergeHistory(obs observations, history *metrics.PressureHistory, owner func(string) (workloadRef, bool)) {
	oomPods := map[containerRef]map[string]bool{}
	for key, h := range history.Containers {
		ref, ok := owner(key.Pod)
		if !ok {
			continue
		}
		cref := containerRef{ref, key.Container}
		p := obs.containers[cref]
		p.ThrottledShare = max(p.ThrottledShare, h.ThrottledShare)
		p.Restarts = max(p.Restarts, int(math.Round(h.Restarts)))
		if h.OOMKilled {
			if oomPods[cref] == nil {
				oomPods[cref] = map[string]bool{}
			}
			oomPods[cref][key.Pod] = true
			p.OOMKills = max(p.OOMKills, len(oomPods[cref]))
		}
		obs.containers[cref] = p
	}
	for pod := range history.EvictedPods {
		if ref, ok := owner(pod); ok {
			obs.workload(ref).evictedPods[pod] = true
		}
	}
}

func peakMemory(w rightsizing.Workload, container string, demand map[metrics.ContainerKey]metrics.Usage) float64 {
	peak := 0.0
	for key, u := range demand {
		if key.Container == container && rightsizing.OwnsPod(w.Kind, w.Name, key.Pod) {
			peak = max(peak, u.Memory.AsApproximateFloat64())
		}
	}
	return peak
}

func holdUnderPressure(workloads []rightsizing.Workload, obs observations, demand map[metrics.ContainerKey]metrics.Usage) {
	for i := range workloads {
		w := &workloads[i]
		ref := workloadRef{w.Kind, w.Name}
		evictions := 0
		if wp, ok := obs.workloads[ref]; ok {
			evictions = len(wp.evictedPods)
		}
		for j := range w.Containers {
			c := &w.Containers[j]
			c.HoldCPU, c.HoldMemory = risks.HoldReductions(obs.containers[containerRef{ref, c.Name}], evictions, peakMemory(*w, c.Name, demand), c.MemoryLimit)
		}
	}
}

func (s *Server) handleRisks(w http.ResponseWriter, r *http.Request) {
	report, err := s.risks(r.Context(), r.PathValue("ns"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) risks(ctx context.Context, ns string) (risks.Report, error) {
	workloads, err := s.workloadRequests(ctx, ns)
	if err != nil {
		return risks.Report{}, err
	}
	if len(workloads) == 0 {
		return risks.Report{Namespace: ns, Risks: []risks.Risk{}}, nil
	}
	obs := s.pressure(ctx, ns, workloads)
	advice, adviceErr := s.recommendations(ctx, ns)
	rates := s.pricingResolver().Rates(ctx)
	report := risks.Report{Namespace: ns, Currency: rates.Currency, Historical: obs.historical, Risks: []risks.Risk{}}
	assess := risks.Context{Namespace: ns, Rates: rates}
	if obs.historical {
		report.Window = promWindow(obs.window)
		assess.Window = report.Window
		report.Basis = fmt.Sprintf("CPU throttling (the p95 of 5-minute shares), OOM kills, restarts and evictions over the last %s from VictoriaMetrics, "+
			"plus what the pods and HorizontalPodAutoscalers report now.", report.Window)
	} else {
		report.Basis = "What the pods and HorizontalPodAutoscalers report now. Connect VictoriaMetrics under Settings → Usage metrics " +
			"for CPU throttling and two weeks of OOM kills, restarts and evictions."
	}
	warnings := slices.Clone(obs.warnings)
	if adviceErr != nil {
		warnings = append(warnings, "memory peaks and JVM findings are missing: "+adviceErr.Error())
	}
	if len(warnings) > 0 {
		report.Warning = warnings[0]
	}
	containerAdvice := map[containerRef]rightsizing.ContainerAdvice{}
	for _, wa := range advice.Workloads {
		for _, ca := range wa.Containers {
			containerAdvice[containerRef{workloadRef{wa.Kind, wa.Name}, ca.Name}] = ca
		}
	}
	for _, w := range workloads {
		ref := workloadRef{w.Kind, w.Name}
		rw := risks.Workload{Kind: w.Kind, Name: w.Name, Replicas: w.Replicas}
		if wp, ok := obs.workloads[ref]; ok {
			rw.Evictions, rw.EvictionMessage, rw.HPA = len(wp.evictedPods), wp.evictionMessage, wp.hpa
		}
		for _, c := range w.Containers {
			cref := containerRef{ref, c.Name}
			ca := containerAdvice[cref]
			rc := risks.Container{
				Name: c.Name, CPURequest: c.CPURequest, CPULimit: c.CPULimit, MemoryRequest: c.MemoryRequest, MemoryLimit: c.MemoryLimit,
				PeakMemory: quantityBytes(ca.Memory.Observed), Pressure: obs.containers[cref],
			}
			if ca.JVM != nil {
				rc.JVMFindings, rc.JVMRecommendedMemory = ca.JVM.Findings, quantityBytes(ca.JVM.RecommendedMemory)
			}
			rw.Containers = append(rw.Containers, rc)
		}
		report.Risks = append(report.Risks, risks.Assess(rw, assess)...)
	}
	report.Risks = append(report.Risks, s.wakeUpRisks(ctx, ns, workloads, rates, time.Now())...)
	risks.Sort(report.Risks)
	return report, nil
}

func (s *Server) scheduleOwner(ctx context.Context, ns string) (client.Object, string, *finopsv1.ScheduleStatus, *finopsv1.Prewarm) {
	var groups finopsv1.ScalingGroupList
	if err := s.Client.List(ctx, &groups, client.InNamespace(config.OperatorNamespace())); err == nil {
		if name, ok := scaling.NamespaceOwners(groups.Items)[ns]; ok {
			for i := range groups.Items {
				if g := &groups.Items[i]; g.Name == name {
					return g, kindScalingGroup, &g.Status.ScheduleStatus, g.Spec.Prewarm
				}
			}
		}
	}
	var configs finopsv1.ScalingConfigList
	if err := s.Client.List(ctx, &configs, client.InNamespace(config.OperatorNamespace())); err == nil {
		for i := range configs.Items {
			c := &configs.Items[i]
			if c.Spec.TargetNamespace == ns {
				return c, kindScalingConfig, &c.Status.ScheduleStatus, c.Spec.Prewarm
			}
		}
	}
	return nil, "", nil, nil
}

func (s *Server) wakeUpRisks(ctx context.Context, ns string, workloads []rightsizing.Workload, rates pricing.Rates, now time.Time) []risks.Risk {
	owner, kind, status, prewarm := s.scheduleOwner(ctx, ns)
	if owner == nil || len(status.RecentWakeUps) == 0 {
		return nil
	}
	slowest := time.Duration(0)
	for _, w := range status.RecentWakeUps {
		slowest = max(slowest, time.Duration(w.Seconds)*time.Second)
	}
	bare := s.timetableOf(ctx, owner)
	bare.Lead = 0
	intervals := bare.Intervals(now, now.AddDate(0, 0, 7))
	starts := 0
	for i := 1; i < len(intervals); i++ {
		if intervals[i].Active && !intervals[i-1].Active {
			starts++
		}
	}
	hourly := 0.0
	for _, w := range workloads {
		for _, c := range w.Containers {
			hourly += (c.CPURequest*rates.CPUCoreHour + c.MemoryRequest/(1<<30)*rates.MemoryGBHour) * float64(max(w.Replicas, 1))
		}
	}
	return risks.AssessWakeUp(risks.Wake{
		Kind: kind, Name: owner.GetName(), Slowest: slowest, Count: len(status.RecentWakeUps),
		Lead: scaling.PrewarmLead(prewarm, status.RecentWakeUps), SuggestedLead: scaling.PrewarmLead(&finopsv1.Prewarm{Auto: true}, status.RecentWakeUps),
		StartsPerMonth: float64(starts) * 30 / 7, HourlyCost: hourly,
	})
}

func quantityBytes(s string) float64 {
	if s == "" {
		return 0
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return q.AsApproximateFloat64()
}
