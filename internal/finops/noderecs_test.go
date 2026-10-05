package finops

import (
	"strings"
	"testing"

	"github.com/migalsp/costdeck-operator/internal/pricing"
)

func poolNode(name, pool, typ string, cpu, mem, reqCPU, reqMem float64) NodeView {
	return NodeView{
		Name: name, Pool: pool, InstanceType: typ, Status: nodeReady, CapacityType: capacityOnDem, Arch: "amd64", Region: "us-east-1",
		CPU: NodeResource{Capacity: cpu, Allocatable: cpu * 0.95, Requested: reqCPU}, Memory: NodeResource{Capacity: mem, Allocatable: mem * 0.9, Requested: reqMem},
		DaemonSetCPU: 0.2, DaemonSetMemGiB: 0.5, MonthlyCost: 280,
	}
}

func TestRecommendNodesMemoryHeavyPool(t *testing.T) {
	// Five general-purpose 8-core nodes, but pods ask for little CPU and much memory.
	rep := NodesReport{Currency: "USD"}
	for i := range 5 {
		rep.Nodes = append(rep.Nodes, poolNode("n"+string(rune('a'+i)), "general", "m5.2xlarge", 8, 32, 1.2+0.2, 14+0.5))
	}
	RecommendNodes(&rep, "aws", nil, 0)
	if len(rep.Recommendations) != 1 {
		t.Fatalf("expected one recommendation, got %+v", rep.Recommendations)
	}
	r := rep.Recommendations[0]
	if r.CurrentType != "m5.2xlarge" || r.CurrentNodes != 5 || !near(r.CurrentMonthly, 5*0.384*730) {
		t.Errorf("current shape wrong: %+v", r)
	}
	if r.Family != "memory" || r.MonthlySavings < r.CurrentMonthly*0.10 || r.Nodes < 2 {
		t.Errorf("a memory-heavy pool should move to a memory shape with at least two nodes: %+v", r)
	}
	if !strings.Contains(r.Reason, "memory-heavy") {
		t.Errorf("reason should name the mix: %s", r.Reason)
	}
	if r.ARM == nil || !strings.HasPrefix(r.ARM.Type, "r7g") || r.ARM.Monthly >= r.Monthly {
		t.Errorf("an arm64 alternative should be cheaper still: %+v", r.ARM)
	}
	found := false
	for _, f := range rep.Findings {
		found = found || (f.Kind == FindingNodeType && f.MonthlySavings == r.MonthlySavings)
	}
	if !found {
		t.Errorf("the recommendation should be a finding: %+v", rep.Findings)
	}
}

func TestRecommendNodesKeepsGoodPoolsAndSkipsSpot(t *testing.T) {
	rep := NodesReport{Currency: "USD"}
	// Already well sized: two m6i.xlarge, nearly full.
	rep.Nodes = append(rep.Nodes,
		poolNode("a", "web", "m6i.xlarge", 4, 16, 3.2, 12), poolNode("b", "web", "m6i.xlarge", 4, 16, 3.2, 12))
	spot := poolNode("s", "batch", "m5.4xlarge", 16, 64, 1, 2)
	spot.CapacityType = capacitySpot
	rep.Nodes = append(rep.Nodes, spot)
	RecommendNodes(&rep, "aws", nil, 1000)
	if len(rep.Recommendations) != 0 {
		t.Errorf("no recommendation expected: %+v", rep.Recommendations)
	}
	if rep.Spot == nil || !near(rep.Spot.MonthlySavings, 600) || rep.Spot.SpotShare <= 0 {
		t.Errorf("spot advice wrong: %+v", rep.Spot)
	}
}

func TestRecommendNodesRegionFactor(t *testing.T) {
	rep := NodesReport{Currency: "USD"}
	for i := range 4 {
		rep.Nodes = append(rep.Nodes, poolNode("n"+string(rune('a'+i)), "p", "m5.4xlarge", 16, 64, 2, 6))
	}
	live := func(region, typ string) (float64, bool) {
		return 0.768 * 1.2, typ == "m5.4xlarge" && region == "us-east-1"
	}
	RecommendNodes(&rep, "aws", live, 0)
	if len(rep.Recommendations) != 1 {
		t.Fatalf("expected a recommendation: %+v", rep.Recommendations)
	}
	r := rep.Recommendations[0]
	if !near(r.CurrentMonthly, 4*0.768*1.2*pricing.HoursPerMonth) {
		t.Errorf("the current pool should be priced live: %v", r.CurrentMonthly)
	}
	if want, _ := LookupInstanceType("aws", r.Type); !near(r.Monthly, float64(r.Nodes)*want.Hourly*1.2*pricing.HoursPerMonth) {
		t.Errorf("candidates should carry the same region factor: %+v", r)
	}
}

func TestAllocatable(t *testing.T) {
	m5, _ := LookupInstanceType("aws", "m5.xlarge") // 4 vCPU, 16 GiB
	cpu, mem := m5.allocatable()
	// 0.06 + 0.01 + 2×0.005 = 0.08 cores; 1 + 0.8 + 0.8 = 2.6 GiB + 0.1 eviction.
	if !near(cpu, 3.92) || !near(mem, 13.3) {
		t.Errorf("allocatable = %v cores, %v GiB", cpu, mem)
	}
}
