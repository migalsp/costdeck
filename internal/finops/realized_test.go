package finops

import (
	"math"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCheckNodes(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	node := func(name string) corev1.Node {
		return corev1.Node{Name: name, Status: corev1.NodeStatus{
			Capacity:    corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")},
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")},
		}}
	}
	pod := func(ns, name, nodeName, cpu, mem string, owner string) corev1.Pod {
		p := corev1.Pod{Namespace: ns, Name: name, Labels: map[string]string{"app": name},
			Spec: corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Name: "c", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}}}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}}
		if owner != "" {
			p.OwnerReferences = []metav1.OwnerReference{{Kind: owner, Name: "o", Controller: new(true)}}
		}
		return p
	}
	nodes := []corev1.Node{node("busy"), node("blocked"), node("idle")}
	pods := []corev1.Pod{
		pod("billing", "api", "busy", "2", "4Gi", "ReplicaSet"),
		pod("billing", "worker", "busy", "1", "2Gi", "ReplicaSet"),
		pod("monitoring", "agent", "busy", "1", "1Gi", "DaemonSet"),
		pod("shop", "db-0", "blocked", "500m", "8Gi", "StatefulSet"),
	}
	pdbs := []policyv1.PodDisruptionBudget{{Namespace: "shop", Name: "db",
		Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db-0"}}}}}
	recorded := []RecordedNode{{"gone", 0.4}, {"busy", 0.2}, {"blocked", 0.2}, {"idle", 0.2}}

	got := CheckNodes(recorded, nodes, pods, pdbs, now)
	if got.Gone != 1 || math.Abs(got.RealizedHourly-0.4) > 1e-9 || got.Blocked != 1 || len(got.Nodes) != 4 {
		t.Fatalf("summary = %+v", got)
	}
	gone, busy, blocked, idle := got.Nodes[0], got.Nodes[1], got.Nodes[2], got.Nodes[3]
	if !gone.Gone || gone.HourlyCost != 0.4 {
		t.Errorf("gone = %+v", gone)
	}
	// The DaemonSet pod leaves with the node, so it is no tenant.
	if busy.Gone || len(busy.Blockers) != 0 || len(busy.Tenants) != 1 || busy.Tenants[0] != (NodeTenancy{Namespace: "billing", Pods: 2, CPU: 3, MemoryGiB: 6}) || busy.RequestedShare != 0.75 {
		t.Errorf("busy = %+v", busy)
	}
	if len(blocked.Blockers) != 1 || blocked.Blockers[0].Reason != BlockerPDB || blocked.RequestedShare != 0.5 {
		t.Errorf("blocked = %+v, want the PDB and memory as the larger share", blocked)
	}
	if idle.Gone || len(idle.Blockers) != 0 || len(idle.Tenants) != 0 || idle.RequestedShare != 0 {
		t.Errorf("idle = %+v", idle)
	}

	if c := NodeHourlyCost(node("x"), 0.03, 0.004); math.Abs(c-(4*0.03+16*0.004)) > 1e-9 {
		t.Errorf("node cost = %v", c)
	}
}
