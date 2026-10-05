package finops

import (
	"maps"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/migalsp/costdeck-operator/internal/pricing"
)

func testNode(name, pool, cpu string, labels map[string]string) corev1.Node {
	l := map[string]string{"karpenter.sh/nodepool": pool, corev1.LabelInstanceTypeStable: "m5.large", corev1.LabelTopologyRegion: "eu-west-1"}
	maps.Copy(l, labels)
	rl := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse("16Gi"), corev1.ResourcePods: resource.MustParse("110")}
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l},
		Status: corev1.NodeStatus{
			Capacity: rl, Allocatable: rl,
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func testPod(ns, name, node, cpu, mem string, daemon bool) corev1.Pod {
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)},
		}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if daemon {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: "DaemonSet", Name: "ds"}}
	}
	return p
}

func TestBuildNodes(t *testing.T) {
	rates := pricing.Rates{CPUCoreHour: 0.04, MemoryGBHour: 0.005, Currency: "USD", Basis: "test"}
	nodes := []corev1.Node{
		testNode("busy", "general", "4", nil),
		testNode("light", "general", "4", nil),
		testNode("spot-1", "batch", "4", map[string]string{"karpenter.sh/capacity-type": capacitySpot}),
	}
	pods := make([]corev1.Pod, 0, 7)
	pods = append(pods,
		testPod("shop", "api", "busy", "2", "8Gi", false),
		testPod("shop", "db", "busy", "500m", "2Gi", false),
		testPod("dev", "web", "light", "500m", "1Gi", false),
		testPod("kube-system", "agent-light", "light", "100m", "128Mi", true),
		testPod("kube-system", "agent-busy", "busy", "100m", "128Mi", true),
		testPod("batch", "job", "spot-1", "1", "2Gi", false),
	)
	pending := testPod("shop", "big", "", "8", "32Gi", false)
	pending.Status = corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
	}}}
	pods = append(pods, pending)
	usage := map[string]corev1.ResourceList{"busy": {corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("4Gi")}}

	rep := BuildNodes(nodes, pods, usage, rates, time.Now())

	if rep.Summary.Nodes != 3 || rep.Summary.Spot != 1 || rep.Summary.PendingPods != 1 || rep.Summary.Pods != 6 {
		t.Errorf("summary wrong: %+v", rep.Summary)
	}
	byName := map[string]NodeView{}
	for _, n := range rep.Nodes {
		byName[n.Name] = n
	}
	busy := byName["busy"]
	if !near(busy.CPU.Requested, 2.6) || !busy.MetricsAvailable || !near(busy.CPU.Used, 1) || busy.Namespaces[0].Namespace != "shop" {
		t.Errorf("busy node wrong: %+v", busy)
	}
	if byName["spot-1"].CapacityType != capacitySpot || byName["busy"].Pool != "general" {
		t.Errorf("pool or capacity type wrong: %+v", byName["spot-1"])
	}
	// light's movable pod (0.5 CPU, 1Gi) fits elsewhere; busy's does not leave the others room.
	if !byName["light"].ConsolidationCandidate || byName["busy"].ConsolidationCandidate {
		t.Errorf("light should be the consolidation candidate: light=%v busy=%v", byName["light"].ConsolidationCandidate, byName["busy"].ConsolidationCandidate)
	}
	if len(rep.Pools) != 2 || rep.Pools[0].Name != "general" || rep.Pools[0].Nodes != 2 {
		t.Errorf("pools wrong: %+v", rep.Pools)
	}
	kinds := map[string]Opportunity{}
	for _, f := range rep.Findings {
		kinds[f.Kind] = f
	}
	if f, ok := kinds[FindingConsolidation]; !ok || !strings.Contains(f.Detail, "light") || !near(f.MonthlySavings, byName["light"].MonthlyCost) {
		t.Errorf("missing consolidation finding: %+v", rep.Findings)
	}
	if _, ok := kinds[FindingPending]; !ok {
		t.Errorf("missing pending pods finding: %+v", rep.Findings)
	}
	if _, ok := kinds[FindingNoSpot]; ok {
		t.Error("a cluster with a spot node must not be told to use spot")
	}
}

func TestNodeShapeAndHealth(t *testing.T) {
	rates := pricing.Rates{CPUCoreHour: 0.04, MemoryGBHour: 0.005, Currency: "USD", Basis: "test"}
	sick := testNode("b", "p", "8", nil)
	sick.Status.Conditions = []corev1.NodeCondition{
		{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
	}
	nodes := []corev1.Node{testNode("a", "p", "8", nil), sick, testNode("c", "p", "8", nil)}
	nodes[2].Status.Conditions[0].Status = corev1.ConditionFalse
	pods := []corev1.Pod{
		testPod("x", "1", "a", "1", "14Gi", false),
		testPod("x", "2", "b", "1", "14Gi", false),
	}
	rep := BuildNodes(nodes, pods, nil, rates, time.Now())
	kinds := map[string]bool{}
	for _, f := range rep.Findings {
		kinds[f.Kind] = true
	}
	for _, k := range []string{FindingShape, FindingNotReady, FindingPressure, FindingNoSpot} {
		if !kinds[k] {
			t.Errorf("missing %s finding in %+v", k, rep.Findings)
		}
	}
	for _, n := range rep.Nodes {
		if n.ConsolidationCandidate {
			t.Errorf("memory-full nodes are no consolidation candidates: %s", n.Name)
		}
	}
}

func TestEmptyFindingsAreLists(t *testing.T) {
	// JSON clients iterate these; a nil slice would arrive as null.
	rep := BuildNodes(nil, nil, nil, pricing.Rates{}, time.Now())
	if rep.Findings == nil || rep.Nodes == nil || rep.Pools == nil {
		t.Errorf("empty node view must use empty lists: %+v", rep)
	}
	if o := NewOverview(Snapshot{}, nil, nil, 0); o.Opportunities == nil || o.Namespaces == nil {
		t.Errorf("empty overview must use empty lists: %+v", o)
	}
}

func TestPodRequestsCountsInitContainersAndOverhead(t *testing.T) {
	p := testPod("x", "y", "n", "100m", "100Mi", false)
	p.Spec.InitContainers = []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("50Mi"),
	}}}}
	p.Spec.Overhead = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")}
	cpu, mem := PodRequests(&p)
	if cpu.MilliValue() != 1010 || mem.Value() != 100<<20 {
		t.Errorf("got cpu %s mem %s", cpu.String(), mem.String())
	}
}
