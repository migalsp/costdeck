package finops

import (
	"fmt"
	"math"
	"sort"

	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// Node recommendations size each pool from what its pods request: the cheapest shape and
// count that fits the requests at a healthy fill, as Kubex Node Optimizer and CAST AI do.
// They compare list prices; discounts apply to either side alike.

const (
	// targetFill is the share of allocatable capacity a recommended pool is sized to.
	targetFill = 0.85
	// minRecommendShare is the smallest saving, as a share of the pool, worth recommending.
	minRecommendShare = 0.10
	// spotDiscount is a conservative spot discount; AWS, Azure and Google Cloud quote 60–90%.
	spotDiscount = 0.6
)

// NodeRecommendation is a cheaper shape for one node pool.
type NodeRecommendation struct {
	Pool           string  `json:"pool"`
	CurrentType    string  `json:"currentType"`
	CurrentNodes   int     `json:"currentNodes"`
	CurrentMonthly float64 `json:"currentMonthly"`
	Type           string  `json:"type"`
	Family         string  `json:"family"`
	Nodes          int     `json:"nodes"`
	Monthly        float64 `json:"monthly"`
	MonthlySavings float64 `json:"monthlySavings"`
	// Requested is what the pool's pods request without DaemonSets, in cores and GiB.
	RequestedCPU float64 `json:"requestedCpu"`
	RequestedMem float64 `json:"requestedMemoryGiB"`
	Reason       string  `json:"reason"`
	// ARM is the cheapest arm64 alternative, for pools whose images also build for arm64.
	ARM *ShapeOption `json:"arm,omitempty"`
}

// ShapeOption is a node shape and count with its monthly price.
type ShapeOption struct {
	Type           string  `json:"type"`
	Nodes          int     `json:"nodes"`
	Monthly        float64 `json:"monthly"`
	MonthlySavings float64 `json:"monthlySavings"`
}

// SpotAdvice estimates what moving non-production workloads to spot capacity saves.
type SpotAdvice struct {
	// SpotShare is the share of the node bill already on spot.
	SpotShare            float64 `json:"spotShare"`
	NonProductionMonthly float64 `json:"nonProductionMonthly"`
	MonthlySavings       float64 `json:"monthlySavings"`
}

// LivePrice returns the live list price of an instance type in a region, when known.
type LivePrice func(region, instanceType string) (float64, bool)

type poolWork struct {
	name                 string
	nodes                []NodeView
	types                map[string]int
	arch                 string
	region               string
	cpu, mem             float64 // requested without DaemonSets
	dsCPU, dsMem         float64 // DaemonSet requests per node
	monthly              float64
	allSpot, haveTypesOK bool
}

// RecommendNodes adds shape recommendations and spot advice to the node view.
// nonProduction is the monthly compute cost of non-production namespaces.
func RecommendNodes(rep *NodesReport, cloud string, live LivePrice, nonProduction float64) {
	rep.Recommendations = []NodeRecommendation{}
	pools := map[string]*poolWork{}
	for _, n := range rep.Nodes {
		if n.Status != nodeReady {
			continue
		}
		name := n.Pool
		if name == "" {
			name = "Unpooled"
		}
		p := pools[name]
		if p == nil {
			p = &poolWork{name: name, types: map[string]int{}, allSpot: true, haveTypesOK: true}
			pools[name] = p
		}
		p.nodes = append(p.nodes, n)
		p.types[n.InstanceType]++
		if n.InstanceType == "" {
			p.haveTypesOK = false
		}
		if n.CapacityType != capacitySpot {
			p.allSpot = false
		}
		if n.Arch != "" {
			p.arch = n.Arch
		}
		if n.Region != "" {
			p.region = n.Region
		}
		p.cpu += max(0, n.CPU.Requested-n.DaemonSetCPU)
		p.mem += max(0, n.Memory.Requested-n.DaemonSetMemGiB)
		p.dsCPU += n.DaemonSetCPU
		p.dsMem += n.DaemonSetMemGiB
		p.monthly += n.MonthlyCost
	}
	names := make([]string, 0, len(pools))
	for name := range pools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := pools[name]
		if p.allSpot || !p.haveTypesOK || p.cpu == 0 && p.mem == 0 {
			continue
		}
		p.dsCPU /= float64(len(p.nodes))
		p.dsMem /= float64(len(p.nodes))
		if rec, ok := recommendPool(p, cloud, live); ok {
			rep.Recommendations = append(rep.Recommendations, rec)
		}
	}
	sort.SliceStable(rep.Recommendations, func(i, j int) bool {
		return rep.Recommendations[i].MonthlySavings > rep.Recommendations[j].MonthlySavings
	})
	rep.Spot = spotAdvice(rep, nonProduction)
	addRecommendationFindings(rep)
}

// addRecommendationFindings turns the recommendations and the spot advice into findings,
// replacing the generic spot hint with numbers when there are some.
func addRecommendationFindings(rep *NodesReport) {
	for _, r := range rep.Recommendations {
		detail := r.Reason
		if r.ARM != nil {
			detail += fmt.Sprintf(" On arm64, %d × %s would save %s if your images support it.", r.ARM.Nodes, r.ARM.Type, money(r.ARM.MonthlySavings, rep.Currency))
		}
		rep.Findings = append(rep.Findings, Opportunity{
			Kind: FindingNodeType, MonthlySavings: r.MonthlySavings,
			Title:  fmt.Sprintf("Pool %s: %d × %s instead of %d × %s", r.Pool, r.Nodes, r.Type, r.CurrentNodes, r.CurrentType),
			Detail: detail,
		})
	}
	if rep.Spot != nil && rep.Spot.MonthlySavings > 0 {
		kept := rep.Findings[:0]
		for _, f := range rep.Findings {
			if f.Kind != FindingNoSpot {
				kept = append(kept, f)
			}
		}
		rep.Findings = append(kept, Opportunity{
			Kind: FindingNoSpot, MonthlySavings: rep.Spot.MonthlySavings,
			Title: "Run non-production workloads on spot capacity",
			Detail: fmt.Sprintf("Non-production namespaces request %s a month of compute, and %.0f%% of the node bill is on spot. "+
				"Spot or preemptible nodes cost 60–90%% less: a spot node pool that only those workloads tolerate would save about %s.",
				money(rep.Spot.NonProductionMonthly, rep.Currency), rep.Spot.SpotShare*100, money(rep.Spot.MonthlySavings, rep.Currency)),
		})
	}
	sort.SliceStable(rep.Findings, func(i, j int) bool { return rep.Findings[i].MonthlySavings > rep.Findings[j].MonthlySavings })
}

func recommendPool(p *poolWork, cloud string, live LivePrice) (NodeRecommendation, bool) {
	arch := p.arch
	if arch == "" {
		arch = "amd64"
	}
	// Price everything relative to the catalog price of the current shape, so a region
	// that costs 10% more makes every candidate 10% dearer too.
	factor := 1.0
	current := NodeRecommendation{Pool: p.name, CurrentNodes: len(p.nodes), CurrentMonthly: p.monthly, RequestedCPU: p.cpu, RequestedMem: p.mem}
	if len(p.types) == 1 {
		for t := range p.types {
			current.CurrentType = t
		}
		if known, ok := LookupInstanceType(cloud, current.CurrentType); ok {
			if live != nil {
				if price, ok := live(p.region, known.Name); ok && price > 0 {
					factor = price / known.Hourly
				}
			}
			current.CurrentMonthly = float64(len(p.nodes)) * known.Hourly * factor * pricing.HoursPerMonth
		}
	} else {
		current.CurrentType = fmt.Sprintf("%d types", len(p.types))
	}
	minNodes := 1
	if len(p.nodes) > 1 {
		minNodes = 2
	}
	best, bestOK := cheapest(p, cloud, arch, factor, minNodes)
	if !bestOK {
		return current, false
	}
	saving := current.CurrentMonthly - best.Monthly
	if saving <= 0 || saving < current.CurrentMonthly*minRecommendShare ||
		(best.Type == current.CurrentType && best.Nodes == current.CurrentNodes) {
		return current, false
	}
	rec := current
	rec.Type, rec.Nodes, rec.Monthly, rec.MonthlySavings = best.Type, best.Nodes, best.Monthly, saving
	if t, ok := LookupInstanceType(cloud, best.Type); ok {
		rec.Family = t.Family
	}
	rec.Reason = fmt.Sprintf("Pods here request %.1f cores and %.0f GiB (%s, %.1f GiB per core). %d × %s hold that at about %.0f%% fill.",
		p.cpu, p.mem, mix(p.cpu, p.mem), p.mem/math.Max(p.cpu, 0.001), best.Nodes, best.Type, targetFill*100)
	if arch == "amd64" {
		if arm, ok := cheapest(p, cloud, "arm64", factor, minNodes); ok && arm.Monthly < best.Monthly*0.97 {
			arm.MonthlySavings = current.CurrentMonthly - arm.Monthly
			rec.ARM = &arm
		}
	}
	return rec, true
}

// cheapest finds the cheapest shape and count of one architecture that fits the pool.
func cheapest(p *poolWork, cloud, arch string, factor float64, minNodes int) (ShapeOption, bool) {
	var best ShapeOption
	found := false
	for _, t := range catalog {
		if t.Cloud != cloud || t.Arch != arch || t.Family == "burstable" {
			continue
		}
		cpu, mem := t.allocatable()
		usableCPU, usableMem := cpu*targetFill-p.dsCPU, mem*targetFill-p.dsMem
		if usableCPU <= 0 || usableMem <= 0 {
			continue
		}
		n := max(minNodes, int(math.Ceil(p.cpu/usableCPU)), int(math.Ceil(p.mem/usableMem)))
		monthly := float64(n) * t.Hourly * factor * pricing.HoursPerMonth
		if !found || monthly < best.Monthly-0.005 || (math.Abs(monthly-best.Monthly) < 0.005 && n < best.Nodes) {
			best, found = ShapeOption{Type: t.Name, Nodes: n, Monthly: monthly}, true
		}
	}
	return best, found
}

// mix names the shape of a workload by its memory per core.
func mix(cpu, mem float64) string {
	switch r := mem / math.Max(cpu, 0.001); {
	case r >= 6:
		return "memory-heavy"
	case r <= 3:
		return "CPU-heavy"
	}
	return "balanced"
}

func spotAdvice(rep *NodesReport, nonProduction float64) *SpotAdvice {
	var spot, total float64
	for _, n := range rep.Nodes {
		total += n.MonthlyCost
		if n.CapacityType == capacitySpot {
			spot += n.MonthlyCost
		}
	}
	a := &SpotAdvice{NonProductionMonthly: nonProduction}
	if total > 0 {
		a.SpotShare = spot / total
	}
	if a.SpotShare < 0.5 && !isLocal(rep.Nodes) {
		a.MonthlySavings = nonProduction * spotDiscount
	}
	return a
}
