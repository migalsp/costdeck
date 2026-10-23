package finops

import (
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
)

// SettleTime is how long autoscalers get to remove the nodes a scale-down emptied before
// the check calls the result: Cluster Autoscaler waits 10 minutes by default
// (--scale-down-unneeded-time), Karpenter usually less.
const SettleTime = 15 * time.Minute

// RecordedNode is a node a schedule's workloads ran on when it began scaling down, with
// what it cost then: a node that has gone can no longer be priced.
type RecordedNode struct {
	Name       string
	HourlyCost float64
}

// NodeCheck says what became of one recorded node.
type NodeCheck struct {
	Name       string  `json:"name"`
	HourlyCost float64 `json:"hourlyCost"`
	// Gone is true once the node no longer exists: its cost is saved.
	Gone bool `json:"gone"`
	// Blockers keep autoscalers from removing a node that is still there.
	Blockers []Blocker `json:"blockers"`
	// Tenants are the namespaces whose pods (DaemonSets aside) still run on it, and
	// RequestedShare the larger of the CPU and memory shares they request.
	Tenants        []NodeTenancy `json:"tenants"`
	RequestedShare float64       `json:"requestedShare"`
}

// NodeCheckSummary sums the checks of a scale-down.
type NodeCheckSummary struct {
	Nodes []NodeCheck `json:"nodes"`
	// Gone and RealizedHourly count the recorded nodes that went away and what they cost.
	Gone           int     `json:"gone"`
	RealizedHourly float64 `json:"realizedHourly"`
	// Blocked counts the nodes still there that something keeps.
	Blocked int `json:"blocked"`
}

// CheckNodes compares the nodes recorded at a scale-down with the cluster now.
func CheckNodes(recorded []RecordedNode, nodes []corev1.Node, pods []corev1.Pod, pdbs []policyv1.PodDisruptionBudget, now time.Time) NodeCheckSummary {
	byName := map[string]*corev1.Node{}
	for i := range nodes {
		byName[nodes[i].Name] = &nodes[i]
	}
	out := NodeCheckSummary{Nodes: []NodeCheck{}}
	for _, r := range recorded {
		c := NodeCheck{Name: r.Name, HourlyCost: r.HourlyCost, Blockers: []Blocker{}, Tenants: []NodeTenancy{}}
		node, ok := byName[r.Name]
		if !ok {
			c.Gone = true
			out.Gone++
			out.RealizedHourly += r.HourlyCost
			out.Nodes = append(out.Nodes, c)
			continue
		}
		c.Blockers = append(c.Blockers, NodeBlockers(*node, pods, pdbs, now)...)
		if len(c.Blockers) > 0 {
			out.Blocked++
		}
		c.Tenants, c.RequestedShare = tenants(node, pods)
		out.Nodes = append(out.Nodes, c)
	}
	return out
}

// tenants sums what the pods on a node request by namespace, DaemonSet pods aside: they
// leave with the node.
func tenants(node *corev1.Node, pods []corev1.Pod) ([]NodeTenancy, float64) {
	per := map[string]*NodeTenancy{}
	var cpu, mem float64
	for i := range pods {
		p := &pods[i]
		if p.Spec.NodeName != node.Name || !active(p) || isDaemonSetPod(p) {
			continue
		}
		c, m := PodRequests(p)
		t := per[p.Namespace]
		if t == nil {
			t = &NodeTenancy{Namespace: p.Namespace}
			per[p.Namespace] = t
		}
		t.Pods++
		t.CPU += c.AsApproximateFloat64()
		t.MemoryGiB += m.AsApproximateFloat64() / gib
		cpu += c.AsApproximateFloat64()
		mem += m.AsApproximateFloat64() / gib
	}
	out := make([]NodeTenancy, 0, len(per))
	for _, t := range per {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CPU != out[j].CPU {
			return out[i].CPU > out[j].CPU
		}
		return out[i].Namespace < out[j].Namespace
	})
	share := 0.0
	if a := node.Status.Allocatable.Cpu().AsApproximateFloat64(); a > 0 {
		share = cpu / a
	}
	if a := node.Status.Allocatable.Memory().AsApproximateFloat64() / gib; a > 0 {
		share = max(share, mem/a)
	}
	return out, share
}

// NodeHourlyCost prices a node at the rates CPU and memory are charged at, as the node view
// does.
func NodeHourlyCost(n corev1.Node, cpuCoreHour, memoryGBHour float64) float64 {
	return n.Status.Capacity.Cpu().AsApproximateFloat64()*cpuCoreHour + n.Status.Capacity.Memory().AsApproximateFloat64()/gib*memoryGBHour
}
