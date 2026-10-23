package finops

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// GPU opportunity kinds.
const (
	OpportunityIdleGPU  = "idle-gpu"
	OpportunityShareGPU = "share-gpu"
	OpportunityGPUModel = "gpu-model"
)

// Thresholds of the GPU advice: below idleGPUShare a GPU is idle; below shareGPUShare a
// whole GPU per workload is more than it needs.
const (
	idleGPUShare  = 0.10
	shareGPUShare = 0.50
	// gpuHeadroom is kept on top of what the GPUs use when sizing what they need.
	gpuHeadroom = 1.25
	// migSlice is the smallest share MIG cuts a GPU into.
	migSlice = 1.0 / 7
)

// gpuModels are the models the cheaper-model advice picks from, with their memory and the
// estimate of what they cost (pricing.HeuristicGPUHour).
var gpuModels = []struct {
	Name      string
	MemoryGiB float64
}{
	{"NVIDIA-T4", 16}, {"NVIDIA-L4", 24}, {"NVIDIA-A10G", 24}, {"NVIDIA-L40S", 48},
	{"NVIDIA-A100-SXM4-40GB", 40}, {"NVIDIA-A100-SXM4-80GB", 80}, {"NVIDIA-H100-80GB-HBM3", 80}, {"NVIDIA-H200", 141},
}

// namespaceGPUs is what one namespace's pods request in GPUs.
type namespaceGPUs struct {
	units  float64
	hourly float64
	models []string
}

// podGPUs sums the GPUs the running pods request per namespace, priced at their node's GPU
// price.
func podGPUs(pods []corev1.Pod, nodes map[string]*corev1.Node, rates pricing.Rates) map[string]namespaceGPUs {
	out := map[string]namespaceGPUs{}
	for i := range pods {
		p := &pods[i]
		if !active(p) {
			continue
		}
		list := PodGPURequests(p)
		if len(list) == 0 {
			continue
		}
		node := nodes[p.Spec.NodeName]
		units := pricing.GPUUnits(list, node)
		if units == 0 {
			continue
		}
		price, model := rates.GPUHour, ""
		if node != nil {
			price, model = rates.NodeGPUHour(*node), pricing.GPUModel(*node)
		}
		g := out[p.Namespace]
		g.units += units
		g.hourly += units * price
		if model != "" && !slices.Contains(g.models, model) {
			g.models = append(g.models, model)
			sort.Strings(g.models)
		}
		out[p.Namespace] = g
	}
	return out
}

// PodGPURequests is the GPU extended resources a pod's containers ask for: their
// requests, or their limits, which the requests default to for extended resources.
func PodGPURequests(p *corev1.Pod) corev1.ResourceList {
	out := corev1.ResourceList{}
	for _, c := range p.Spec.Containers {
		for _, list := range []corev1.ResourceList{c.Resources.Requests, c.Resources.Limits} {
			found := false
			for name, q := range list {
				if !isGPUResource(name) {
					continue
				}
				found = true
				sum := out[name]
				sum.Add(q)
				out[name] = sum
			}
			if found {
				break
			}
		}
	}
	return out
}

func isGPUResource(name corev1.ResourceName) bool {
	return name == pricing.ResourceNvidiaGPU || name == pricing.ResourceNvidiaShared || name == pricing.ResourceAMDGPU ||
		strings.HasPrefix(string(name), "nvidia.com/mig-")
}

// gpuOpportunities advises on the namespaces whose GPUs DCGM reports: GPUs that sit idle,
// GPUs that several workloads could share (MIG or time-slicing), and GPUs with far more
// memory than the workload uses.
func gpuOpportunities(s Snapshot) []Opportunity {
	var out []Opportunity
	m := func(hourly float64) float64 { return hourly * pricing.HoursPerMonth }
	for _, ns := range s.Namespaces {
		if ns.GPUHourly == 0 || ns.GPUUsage == nil {
			continue
		}
		u := ns.GPUUsage
		perGPU := ns.GPUHourly / ns.GPURequested
		pct := math.Round(u.Utilization * 100)
		switch {
		case u.Utilization < idleGPUShare:
			out = append(out, Opportunity{Kind: OpportunityIdleGPU, Namespace: ns.Name,
				Title:          fmt.Sprintf("GPUs in %s are nearly idle", ns.Name),
				Detail:         fmt.Sprintf("%s requested, busy %.0f%% of the last day. Release them, or scale the workload to zero outside working hours.", gpuCount(ns.GPURequested), pct),
				MonthlySavings: m(ns.GPUHourly) * (1 - u.Utilization)})
			continue
		case u.Utilization < shareGPUShare && ns.GPURequested >= 1:
			need := max(migSlice, u.Utilization*ns.GPURequested*gpuHeadroom)
			if saved := (ns.GPURequested - need) * perGPU; saved > 0 {
				out = append(out, Opportunity{Kind: OpportunityShareGPU, Namespace: ns.Name,
					Title: fmt.Sprintf("Share the GPUs in %s", ns.Name),
					Detail: fmt.Sprintf("%s requested, busy %.0f%% of the last day: about %s would do. MIG slices or time-slicing let several workloads share one GPU.",
						gpuCount(ns.GPURequested), pct, gpuCount(need)),
					MonthlySavings: m(saved)})
			}
		}
		if alt, saved, ok := cheaperGPU(ns, perGPU); ok {
			out = append(out, Opportunity{Kind: OpportunityGPUModel, Namespace: ns.Name,
				Title: fmt.Sprintf("A smaller GPU would do for %s", ns.Name),
				Detail: fmt.Sprintf("Its GPUs (%s) held at most %.1f GiB of memory over the last week; %s has enough. Check it is fast enough for the workload.",
					strings.Join(ns.GPUModels, ", "), u.MemoryUsedGiB, alt),
				MonthlySavings: m(saved)})
		}
	}
	return out
}

// cheaperGPU picks the cheapest model with enough memory for what a namespace's GPUs use,
// when it costs less than they do.
func cheaperGPU(ns Namespace, perGPU float64) (string, float64, bool) {
	if ns.GPUUsage.MemoryUsedGiB <= 0 || len(ns.GPUModels) != 1 {
		return "", 0, false
	}
	need := ns.GPUUsage.MemoryUsedGiB * gpuHeadroom
	best, bestPrice := "", perGPU
	for _, g := range gpuModels {
		if p := pricing.HeuristicGPUHour(g.Name); g.MemoryGiB >= need && p < bestPrice && !strings.EqualFold(g.Name, ns.GPUModels[0]) {
			best, bestPrice = g.Name, p
		}
	}
	if best == "" {
		return "", 0, false
	}
	return best, (perGPU - bestPrice) * ns.GPURequested, true
}

func gpuCount(n float64) string {
	if n == 1 {
		return "1 GPU"
	}
	if n == math.Trunc(n) {
		return fmt.Sprintf("%.0f GPUs", n)
	}
	return fmt.Sprintf("%.2g GPUs", n)
}
