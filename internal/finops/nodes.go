package finops

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// The node view answers the infrastructure half of the cost question: what each node costs,
// how well pod requests fill it (bin-packing), and which nodes could go.

const (
	// consolidationShare is the request level under which a node is worth emptying.
	consolidationShare = 0.5
	// headroom is kept free on the nodes that would take over the pods of a removed one.
	headroom = 0.1
	// shapeSkew flags a cluster whose CPU and memory requests fill the nodes this unevenly.
	shapeSkew         = 2.0
	maxNodeNamespaces = 8
)

// Node pool labels of the managed Kubernetes services and Karpenter, in order of preference.
var poolLabels = []string{
	"karpenter.sh/nodepool", "eks.amazonaws.com/nodegroup", "cloud.google.com/gke-nodepool",
	"kubernetes.azure.com/agentpool", "agentpool", "node.kubernetes.io/pool", "nodepool",
}

// NodesReport is the node view.
type NodesReport struct {
	GeneratedAt time.Time     `json:"generatedAt"`
	Currency    string        `json:"currency"`
	Rates       pricing.Rates `json:"rates"`
	Summary     NodesSummary  `json:"summary"`
	Pools       []PoolSummary `json:"pools"`
	Nodes       []NodeView    `json:"nodes"`
	Findings    []Opportunity `json:"findings"`
	// Recommendations and Spot are set by RecommendNodes.
	Recommendations []NodeRecommendation `json:"recommendations"`
	Spot            *SpotAdvice          `json:"spot,omitempty"`
}

// NodesSummary sums the nodes.
type NodesSummary struct {
	Nodes         int          `json:"nodes"`
	Ready         int          `json:"ready"`
	Spot          int          `json:"spot"`
	Unschedulable int          `json:"unschedulable"`
	PendingPods   int          `json:"pendingPods"`
	Pods          int          `json:"pods"`
	MaxPods       int          `json:"maxPods"`
	CPU           NodeResource `json:"cpu"`
	Memory        NodeResource `json:"memoryGiB"`
	// MonthlyCost is what the nodes cost; MonthlyUnrequested the part no pod requests.
	MonthlyCost        float64 `json:"monthlyCost"`
	MonthlyUnrequested float64 `json:"monthlyUnrequested"`
}

// NodeResource is one resource of a node or of all nodes, in cores or GiB.
type NodeResource struct {
	Capacity    float64 `json:"capacity"`
	Allocatable float64 `json:"allocatable"`
	Requested   float64 `json:"requested"`
	Used        float64 `json:"used"`
}

// PoolSummary groups the nodes of one node pool.
type PoolSummary struct {
	Name          string       `json:"name"`
	Nodes         int          `json:"nodes"`
	Spot          int          `json:"spot"`
	InstanceTypes []string     `json:"instanceTypes"`
	CPU           NodeResource `json:"cpu"`
	Memory        NodeResource `json:"memoryGiB"`
	MonthlyCost   float64      `json:"monthlyCost"`
}

// NodeView is one node.
type NodeView struct {
	Name          string    `json:"name"`
	Status        string    `json:"status"` // Ready, NotReady, Unknown
	Unschedulable bool      `json:"unschedulable"`
	Pressure      []string  `json:"pressure,omitempty"`
	Pool          string    `json:"pool,omitempty"`
	InstanceType  string    `json:"instanceType,omitempty"`
	Zone          string    `json:"zone,omitempty"`
	Region        string    `json:"region,omitempty"`
	CapacityType  string    `json:"capacityType"` // spot or on-demand
	Arch          string    `json:"arch,omitempty"`
	OS            string    `json:"os,omitempty"`
	Kernel        string    `json:"kernel,omitempty"`
	Kubelet       string    `json:"kubelet,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	Pods          int       `json:"pods"`
	MaxPods       int       `json:"maxPods"`
	// MetricsAvailable is false when metrics-server has no reading for the node.
	MetricsAvailable bool          `json:"metricsAvailable"`
	CPU              NodeResource  `json:"cpu"`
	Memory           NodeResource  `json:"memoryGiB"`
	MonthlyCost      float64       `json:"monthlyCost"`
	MonthlyRequested float64       `json:"monthlyRequested"`
	MonthlyUsed      float64       `json:"monthlyUsed"`
	Namespaces       []NodeTenancy `json:"namespaces"`
	// ConsolidationCandidate marks a node whose pods would fit on the others.
	ConsolidationCandidate bool `json:"consolidationCandidate,omitempty"`
	// DaemonSetCPU and DaemonSetMemGiB are the requests of DaemonSet pods, which every
	// node of a pool carries.
	DaemonSetCPU    float64 `json:"daemonSetCpu"`
	DaemonSetMemGiB float64 `json:"daemonSetMemoryGiB"`
}

// NodeTenancy is what one namespace requests on a node.
type NodeTenancy struct {
	Namespace string  `json:"namespace"`
	Pods      int     `json:"pods"`
	CPU       float64 `json:"cpu"`
	MemoryGiB float64 `json:"memoryGiB"`
}

// Node states and capacity types.
const (
	nodeReady     = "Ready"
	nodeNotReady  = "NotReady"
	capacitySpot  = "spot"
	capacityOnDem = "on-demand"
)

// Opportunity kinds of the node view.
const (
	FindingConsolidation = "consolidation"
	FindingShape         = "node-shape"
	FindingPending       = "pending-pods"
	FindingNotReady      = "node-not-ready"
	FindingPressure      = "node-pressure"
	FindingNoSpot        = "no-spot"
	FindingNodeType      = "node-type"
)

// PodRequests is what the scheduler reserves for a pod: the larger of its containers'
// requests and its biggest init container, plus the pod overhead.
func PodRequests(p *corev1.Pod) (cpu, mem resource.Quantity) {
	for _, c := range p.Spec.Containers {
		cpu.Add(*c.Resources.Requests.Cpu())
		mem.Add(*c.Resources.Requests.Memory())
	}
	for _, c := range p.Spec.InitContainers {
		if c.Resources.Requests.Cpu().Cmp(cpu) > 0 {
			cpu = c.Resources.Requests.Cpu().DeepCopy()
		}
		if c.Resources.Requests.Memory().Cmp(mem) > 0 {
			mem = c.Resources.Requests.Memory().DeepCopy()
		}
	}
	if p.Spec.Overhead != nil {
		cpu.Add(*p.Spec.Overhead.Cpu())
		mem.Add(*p.Spec.Overhead.Memory())
	}
	return cpu, mem
}

// active reports whether a pod holds node resources.
func active(p *corev1.Pod) bool {
	return p.Spec.NodeName != "" && p.Status.Phase != corev1.PodSucceeded && p.Status.Phase != corev1.PodFailed
}

// Unschedulable reports whether a pod is pending because no node can take it.
func Unschedulable(p *corev1.Pod) bool {
	if p.Status.Phase != corev1.PodPending || p.Spec.NodeName != "" {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return true
		}
	}
	return false
}

func isDaemonSetPod(p *corev1.Pod) bool {
	for _, o := range p.OwnerReferences {
		if o.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

// BuildNodes assembles the node view. usage holds metrics-server readings by node name.
func BuildNodes(nodes []corev1.Node, pods []corev1.Pod, usage map[string]corev1.ResourceList, rates pricing.Rates, now time.Time) NodesReport {
	rep := NodesReport{GeneratedAt: now, Currency: rates.Currency, Rates: rates, Nodes: []NodeView{}, Pools: []PoolSummary{},
		Findings: []Opportunity{}, Recommendations: []NodeRecommendation{}}

	type tenancy struct {
		pods     int
		cpu, mem float64
	}
	perNode := map[string]map[string]*tenancy{}
	podsOn := map[string]int{}
	// movable is what would have to find room elsewhere if the node went: everything but
	// DaemonSet pods, which run on every node anyway.
	movableCPU, movableMem := map[string]float64{}, map[string]float64{}
	dsCPU, dsMem := map[string]float64{}, map[string]float64{}
	for i := range pods {
		p := &pods[i]
		if Unschedulable(p) {
			rep.Summary.PendingPods++
		}
		if !active(p) {
			continue
		}
		cpu, mem := PodRequests(p)
		c, m := cpu.AsApproximateFloat64(), mem.AsApproximateFloat64()/gib
		if perNode[p.Spec.NodeName] == nil {
			perNode[p.Spec.NodeName] = map[string]*tenancy{}
		}
		t := perNode[p.Spec.NodeName][p.Namespace]
		if t == nil {
			t = &tenancy{}
			perNode[p.Spec.NodeName][p.Namespace] = t
		}
		t.pods++
		t.cpu += c
		t.mem += m
		podsOn[p.Spec.NodeName]++
		if isDaemonSetPod(p) {
			dsCPU[p.Spec.NodeName] += c
			dsMem[p.Spec.NodeName] += m
		} else {
			movableCPU[p.Spec.NodeName] += c
			movableMem[p.Spec.NodeName] += m
		}
	}

	for _, n := range nodes {
		v := NodeView{
			Name: n.Name, Status: "Unknown", Unschedulable: n.Spec.Unschedulable,
			InstanceType: n.Labels[corev1.LabelInstanceTypeStable], Zone: n.Labels[corev1.LabelTopologyZone],
			Region: n.Labels[corev1.LabelTopologyRegion], CapacityType: capacityOnDem,
			Arch: n.Status.NodeInfo.Architecture, OS: n.Status.NodeInfo.OSImage, Kernel: n.Status.NodeInfo.KernelVersion,
			Kubelet: n.Status.NodeInfo.KubeletVersion, CreatedAt: n.CreationTimestamp.Time,
			Pods: podsOn[n.Name], MaxPods: int(n.Status.Allocatable.Pods().Value()), Namespaces: []NodeTenancy{},
			DaemonSetCPU: dsCPU[n.Name], DaemonSetMemGiB: dsMem[n.Name],
		}
		for _, key := range poolLabels {
			if p := n.Labels[key]; p != "" {
				v.Pool = p
				break
			}
		}
		if pricing.IsSpot(n) || n.Labels["cloud.google.com/gke-spot"] == "true" || n.Labels["cloud.google.com/gke-preemptible"] == "true" {
			v.CapacityType = capacitySpot
		}
		for _, c := range n.Status.Conditions {
			switch {
			case c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue:
				v.Status = nodeReady
			case c.Type == corev1.NodeReady:
				v.Status = nodeNotReady
			case c.Status == corev1.ConditionTrue && (c.Type == corev1.NodeMemoryPressure || c.Type == corev1.NodeDiskPressure ||
				c.Type == corev1.NodePIDPressure || c.Type == corev1.NodeNetworkUnavailable):
				v.Pressure = append(v.Pressure, string(c.Type))
			}
		}
		v.CPU = NodeResource{Capacity: n.Status.Capacity.Cpu().AsApproximateFloat64(), Allocatable: n.Status.Allocatable.Cpu().AsApproximateFloat64()}
		v.Memory = NodeResource{Capacity: n.Status.Capacity.Memory().AsApproximateFloat64() / gib, Allocatable: n.Status.Allocatable.Memory().AsApproximateFloat64() / gib}
		for ns, t := range perNode[n.Name] {
			v.CPU.Requested += t.cpu
			v.Memory.Requested += t.mem
			v.Namespaces = append(v.Namespaces, NodeTenancy{Namespace: ns, Pods: t.pods, CPU: t.cpu, MemoryGiB: t.mem})
		}
		sort.Slice(v.Namespaces, func(i, j int) bool {
			ci := v.Namespaces[i].CPU*rates.CPUCoreHour + v.Namespaces[i].MemoryGiB*rates.MemoryGBHour
			cj := v.Namespaces[j].CPU*rates.CPUCoreHour + v.Namespaces[j].MemoryGiB*rates.MemoryGBHour
			if ci != cj {
				return ci > cj
			}
			return v.Namespaces[i].Namespace < v.Namespaces[j].Namespace
		})
		if len(v.Namespaces) > maxNodeNamespaces {
			v.Namespaces = v.Namespaces[:maxNodeNamespaces]
		}
		if u, ok := usage[n.Name]; ok {
			v.MetricsAvailable = true
			v.CPU.Used = u.Cpu().AsApproximateFloat64()
			v.Memory.Used = u.Memory().AsApproximateFloat64() / gib
		}
		month := func(cpu, mem float64) float64 {
			return (cpu*rates.CPUCoreHour + mem*rates.MemoryGBHour) * pricing.HoursPerMonth
		}
		v.MonthlyCost = month(v.CPU.Capacity, v.Memory.Capacity)
		v.MonthlyRequested = month(v.CPU.Requested, v.Memory.Requested)
		v.MonthlyUsed = month(v.CPU.Used, v.Memory.Used)
		rep.Nodes = append(rep.Nodes, v)
	}
	sort.Slice(rep.Nodes, func(i, j int) bool {
		if rep.Nodes[i].Pool != rep.Nodes[j].Pool {
			return rep.Nodes[i].Pool < rep.Nodes[j].Pool
		}
		return rep.Nodes[i].Name < rep.Nodes[j].Name
	})

	markConsolidation(rep.Nodes, movableCPU, movableMem)
	rep.Summary, rep.Pools = summarise(rep.Nodes, rep.Summary.PendingPods)
	rep.Findings = nodeFindings(rep)
	return rep
}

// markConsolidation flags lightly requested nodes whose movable pods would fit into the
// free requests of the remaining nodes, emptiest first. It ignores affinity, taints,
// volumes and disruption budgets, so it names candidates, not certainties.
func markConsolidation(nodes []NodeView, movableCPU, movableMem map[string]float64) {
	schedulable := 0
	var freeCPU, freeMem float64
	for _, n := range nodes {
		if n.Status == nodeReady && !n.Unschedulable {
			schedulable++
			freeCPU += max(0, n.CPU.Allocatable*(1-headroom)-n.CPU.Requested)
			freeMem += max(0, n.Memory.Allocatable*(1-headroom)-n.Memory.Requested)
		}
	}
	if schedulable < 2 {
		return
	}
	order := make([]int, 0, len(nodes))
	for i, n := range nodes {
		if n.Status != nodeReady || n.Unschedulable || n.CPU.Allocatable == 0 || n.Memory.Allocatable == 0 {
			continue
		}
		if n.CPU.Requested/n.CPU.Allocatable <= consolidationShare && n.Memory.Requested/n.Memory.Allocatable <= consolidationShare {
			order = append(order, i)
		}
	}
	sort.Slice(order, func(a, b int) bool { return nodes[order[a]].MonthlyRequested < nodes[order[b]].MonthlyRequested })
	removed := 0
	for _, i := range order {
		if schedulable-removed < 2 {
			break
		}
		n := &nodes[i]
		// The node's own free room leaves with it.
		ownCPU := max(0, n.CPU.Allocatable*(1-headroom)-n.CPU.Requested)
		ownMem := max(0, n.Memory.Allocatable*(1-headroom)-n.Memory.Requested)
		needCPU, needMem := movableCPU[n.Name], movableMem[n.Name]
		if needCPU <= freeCPU-ownCPU && needMem <= freeMem-ownMem {
			n.ConsolidationCandidate = true
			freeCPU -= ownCPU + needCPU
			freeMem -= ownMem + needMem
			removed++
		}
	}
}

func summarise(nodes []NodeView, pending int) (NodesSummary, []PoolSummary) {
	s := NodesSummary{Nodes: len(nodes), PendingPods: pending}
	pools := map[string]*PoolSummary{}
	types := map[string]map[string]bool{}
	add := func(dst *NodeResource, r NodeResource) {
		dst.Capacity += r.Capacity
		dst.Allocatable += r.Allocatable
		dst.Requested += r.Requested
		dst.Used += r.Used
	}
	for _, n := range nodes {
		if n.Status == nodeReady {
			s.Ready++
		}
		if n.CapacityType == capacitySpot {
			s.Spot++
		}
		if n.Unschedulable {
			s.Unschedulable++
		}
		s.Pods += n.Pods
		s.MaxPods += n.MaxPods
		add(&s.CPU, n.CPU)
		add(&s.Memory, n.Memory)
		s.MonthlyCost += n.MonthlyCost
		s.MonthlyUnrequested += max(0, n.MonthlyCost-n.MonthlyRequested)

		name := n.Pool
		if name == "" {
			name = "Unpooled"
		}
		p := pools[name]
		if p == nil {
			p = &PoolSummary{Name: name}
			pools[name] = p
			types[name] = map[string]bool{}
		}
		p.Nodes++
		if n.CapacityType == capacitySpot {
			p.Spot++
		}
		if n.InstanceType != "" {
			types[name][n.InstanceType] = true
		}
		add(&p.CPU, n.CPU)
		add(&p.Memory, n.Memory)
		p.MonthlyCost += n.MonthlyCost
	}
	out := make([]PoolSummary, 0, len(pools))
	for name, p := range pools {
		p.InstanceTypes = []string{}
		for t := range types[name] {
			p.InstanceTypes = append(p.InstanceTypes, t)
		}
		sort.Strings(p.InstanceTypes)
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MonthlyCost > out[j].MonthlyCost })
	return s, out
}

func nodeFindings(rep NodesReport) []Opportunity {
	out := []Opportunity{}
	var candidates []string
	var saving float64
	for _, n := range rep.Nodes {
		if n.ConsolidationCandidate {
			candidates = append(candidates, n.Name)
			saving += n.MonthlyCost
		}
	}
	if len(candidates) > 0 {
		out = append(out, Opportunity{
			Kind: FindingConsolidation, MonthlySavings: saving,
			Title: fmt.Sprintf("%d node%s could be emptied", len(candidates), plural(len(candidates))),
			Detail: fmt.Sprintf("The pods on %s would fit into what the other nodes have left. Cluster Autoscaler or Karpenter consolidation "+
				"removes such nodes; check affinity, local volumes and disruption budgets first.", list(candidates, 3)),
		})
	}
	s := rep.Summary
	if s.CPU.Allocatable > 0 && s.Memory.Allocatable > 0 {
		cpuShare, memShare := s.CPU.Requested/s.CPU.Allocatable, s.Memory.Requested/s.Memory.Allocatable
		switch {
		case memShare >= 0.5 && memShare >= shapeSkew*cpuShare:
			out = append(out, Opportunity{
				Kind:   FindingShape,
				Title:  fmt.Sprintf("Memory runs out first: %.0f%% requested against %.0f%% of CPU", memShare*100, cpuShare*100),
				Detail: "Nodes are added for memory while their CPU sits idle. Instance types with more memory per core (memory-optimised families) would need fewer nodes.",
			})
		case cpuShare >= 0.5 && cpuShare >= shapeSkew*memShare:
			out = append(out, Opportunity{
				Kind:   FindingShape,
				Title:  fmt.Sprintf("CPU runs out first: %.0f%% requested against %.0f%% of memory", cpuShare*100, memShare*100),
				Detail: "Nodes are added for CPU while their memory sits idle. Compute-optimised instance types (more cores per GiB) would need fewer nodes.",
			})
		}
	}
	if s.PendingPods > 0 {
		out = append(out, Opportunity{
			Kind:   FindingPending,
			Title:  fmt.Sprintf("%d pod%s cannot be scheduled", s.PendingPods, plural(s.PendingPods)),
			Detail: "No node has room for their requests, or their constraints match no node. They are not running, and an autoscaler would add a node for them.",
		})
	}
	var notReady, pressure []string
	for _, n := range rep.Nodes {
		if n.Status != nodeReady {
			notReady = append(notReady, n.Name)
		}
		if len(n.Pressure) > 0 {
			pressure = append(pressure, fmt.Sprintf("%s (%s)", n.Name, list(n.Pressure, 2)))
		}
	}
	if len(notReady) > 0 {
		out = append(out, Opportunity{
			Kind: FindingNotReady, Title: fmt.Sprintf("%d node%s not ready", len(notReady), plural(len(notReady))),
			Detail: fmt.Sprintf("%s still cost money but run nothing new.", list(notReady, 3)),
		})
	}
	if len(pressure) > 0 {
		out = append(out, Opportunity{
			Kind: FindingPressure, Title: fmt.Sprintf("%d node%s under pressure", len(pressure), plural(len(pressure))),
			Detail: fmt.Sprintf("%s. The kubelet evicts pods when a node runs short; requests below real usage are the usual cause.", list(pressure, 3)),
		})
	}
	if s.Nodes >= 3 && s.Spot == 0 && rep.Rates.Basis != "" && !isLocal(rep.Nodes) {
		out = append(out, Opportunity{
			Kind:   FindingNoSpot,
			Title:  "No spot capacity",
			Detail: "Spot or preemptible nodes cost 60–90% less. Stateless and non-production workloads that tolerate interruption are good candidates for a spot node pool.",
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].MonthlySavings > out[j].MonthlySavings })
	return out
}

// isLocal is true for clusters without cloud instance types, where spot does not exist.
func isLocal(nodes []NodeView) bool {
	for _, n := range nodes {
		if n.InstanceType != "" && n.Region != "" {
			return false
		}
	}
	return true
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func list(items []string, limit int) string {
	if len(items) <= limit {
		return joinAnd(items)
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:limit], ", "), len(items)-limit)
}

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
