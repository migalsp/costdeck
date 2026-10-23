package finops

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

const a10g = 0.81 // pricing.HeuristicGPUHour("NVIDIA-A10G")

func gpuCluster(t *testing.T, gpuPods map[string]string) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	capacity := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")}
	cpuNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cpu"}, Status: corev1.NodeStatus{Capacity: capacity, Allocatable: capacity}}
	gpuCap := capacity.DeepCopy()
	gpuCap[pricing.ResourceNvidiaGPU] = resource.MustParse("4")
	gpuNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "gpu", Labels: map[string]string{"nvidia.com/gpu.product": "NVIDIA-A10G"}},
		Status: corev1.NodeStatus{Capacity: gpuCap, Allocatable: gpuCap}}
	objs := make([]client.Object, 0, 6+len(gpuPods))
	objs = append(objs, cpuNode, gpuNode)
	for _, ns := range []string{"ml", "web"} {
		objs = append(objs, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
			&finopsv1.NamespaceFinOps{ObjectMeta: metav1.ObjectMeta{Name: ns, Namespace: "costdeck"}, Spec: finopsv1.NamespaceFinOpsSpec{TargetNamespace: ns},
				Status: finopsv1.NamespaceFinOpsStatus{History: []finopsv1.MetricDataPoint{{CPU: finopsv1.ResourceMetrics{Requests: "1", Usage: "1"}, Memory: finopsv1.ResourceMetrics{Requests: "1Gi", Usage: "1Gi"}}}}})
	}
	for name, gpus := range gpuPods {
		// Extended resources may be given as limits only; the request defaults to them.
		objs = append(objs, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ml", Name: name},
			Spec: corev1.PodSpec{NodeName: "gpu", Containers: []corev1.Container{{Name: "c", Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{pricing.ResourceNvidiaGPU: resource.MustParse(gpus)}}}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestGPUCostInTheSnapshot(t *testing.T) {
	rates := pricing.Rates{CPUCoreHour: 0.033, MemoryGBHour: 0.00375, Currency: usd}
	c := gpuCluster(t, map[string]string{"train": "2"})
	s, err := Build(context.Background(), c, rates, Options{Namespace: "costdeck", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if s.Cluster.GPUs != 4 || !near(s.Cluster.GPUHourly, 4*a10g) || s.Cluster.GPURequested != 2 {
		t.Errorf("cluster GPUs = %v, %v/h, %v requested", s.Cluster.GPUs, s.Cluster.GPUHourly, s.Cluster.GPURequested)
	}
	if !near(s.Cluster.ProvisionedHourly, 8*0.033+32*0.00375+4*a10g) {
		t.Errorf("the GPUs are missing from the bill: %v", s.Cluster.ProvisionedHourly)
	}
	ml, web := s.Namespaces[0], s.Namespaces[1]
	base := 0.033 + 0.00375
	if ml.GPURequested != 2 || !near(ml.GPUHourly, 2*a10g) || !near(ml.RequestedHourly, base+2*a10g) || strings.Join(ml.GPUModels, ",") != "NVIDIA-A10G" {
		t.Errorf("ml = %+v", ml)
	}
	// Without DCGM a GPU counts as used: nothing says it is idle.
	if !near(ml.UsedHourly, base+2*a10g) || !near(ml.IdleHourly, 0) || ml.GPUUsage != nil {
		t.Errorf("ml without DCGM: used %v, idle %v", ml.UsedHourly, ml.IdleHourly)
	}
	if web.GPURequested != 0 || !near(web.RequestedHourly, base) {
		t.Errorf("web = %+v", web)
	}

	s, _ = Build(context.Background(), c, rates, Options{Namespace: "costdeck", Now: time.Now(), GPUUsage: map[string]metrics.GPUUsage{"ml": {Utilization: 0.25, MemoryUsedGiB: 20}}})
	ml = s.Namespaces[0]
	if !near(ml.UsedHourly, base+0.5*a10g) || !near(ml.IdleHourly, 1.5*a10g) {
		t.Errorf("ml at 25%%: used %v, idle %v", ml.UsedHourly, ml.IdleHourly)
	}
	o := NewOverview(s, nil, nil, 0)
	row := rowOf(t, o, "ml")
	if row.GPU == nil || row.GPU.Requested != 2 || !near(row.GPU.MonthlyCost, 2*a10g*730) || *row.GPU.Utilization != 0.25 || *row.GPU.MemoryUsedGiB != 20 {
		t.Errorf("ml row GPU = %+v", row.GPU)
	}
	if !near(o.Monthly.GPU, 4*a10g*730) || !near(o.Monthly.IdleGPU, 1.5*a10g*730) || rowOf(t, o, "web").GPU != nil {
		t.Errorf("monthly GPU %v, idle %v", o.Monthly.GPU, o.Monthly.IdleGPU)
	}
}

func TestGPUAdvice(t *testing.T) {
	ns := func(gpus, util, memGiB float64, models ...string) Namespace {
		return Namespace{Name: "ml", GPURequested: gpus, GPUHourly: gpus * a10g, GPUModels: models, GPUUsage: &metrics.GPUUsage{Utilization: util, MemoryUsedGiB: memGiB}}
	}
	kinds := func(ops []Opportunity) string {
		out := make([]string, 0, len(ops))
		for _, o := range ops {
			out = append(out, o.Kind)
		}
		return strings.Join(out, ",")
	}
	m := 730.0

	idle := gpuOpportunities(Snapshot{Namespaces: []Namespace{ns(2, 0.05, 20, "NVIDIA-A10G")}})
	if kinds(idle) != OpportunityIdleGPU {
		t.Fatalf("idle = %s", kinds(idle))
	}
	if !near(idle[0].MonthlySavings, 2*a10g*m*0.95) || !strings.Contains(idle[0].Detail, "2 GPUs requested, busy 5%") {
		t.Errorf("idle = %+v", idle[0])
	}

	// Two GPUs at 30% need about 0.75 of one with headroom: MIG or time-slicing.
	share := gpuOpportunities(Snapshot{Namespaces: []Namespace{ns(2, 0.30, 20, "NVIDIA-A10G")}})
	if kinds(share) != OpportunityShareGPU || !near(share[0].MonthlySavings, (2-0.75)*a10g*m) || !strings.Contains(share[0].Detail, "about 0.75 GPUs would do") {
		t.Errorf("share = %s %+v", kinds(share), share)
	}

	// 3 GiB of an A10G's 24 fits a T4 (16 GiB), the cheapest that does.
	small := gpuOpportunities(Snapshot{Namespaces: []Namespace{ns(1, 0.80, 3, "NVIDIA-A10G")}})
	if kinds(small) != OpportunityGPUModel || !strings.Contains(small[0].Detail, "NVIDIA-T4 has enough") || !near(small[0].MonthlySavings, (a10g-0.33)*m) {
		t.Errorf("smaller model = %s %+v", kinds(small), small)
	}
	// 20 GiB needs 25 with headroom: nothing cheaper than an A10G has that much.
	if got := gpuOpportunities(Snapshot{Namespaces: []Namespace{ns(1, 0.80, 20, "NVIDIA-A10G")}}); len(got) != 0 {
		t.Errorf("busy GPU with its memory in use: %s", kinds(got))
	}
	// Without DCGM, or with mixed models, there is no advice to give.
	if got := gpuOpportunities(Snapshot{Namespaces: []Namespace{{Name: "ml", GPURequested: 1, GPUHourly: a10g}}}); len(got) != 0 {
		t.Errorf("advice without usage: %s", kinds(got))
	}
	if got := gpuOpportunities(Snapshot{Namespaces: []Namespace{ns(2, 0.80, 3, "NVIDIA-A10G", "NVIDIA-L4")}}); len(got) != 0 {
		t.Errorf("model advice for mixed models: %s", kinds(got))
	}
}

func TestGPUNodesInTheNodeView(t *testing.T) {
	q := resource.MustParse
	capacity := corev1.ResourceList{corev1.ResourceCPU: q("8"), corev1.ResourceMemory: q("32Gi"), corev1.ResourcePods: q("110")}
	node := func(name string, gpus string) corev1.Node {
		c := capacity.DeepCopy()
		labels := map[string]string{}
		if gpus != "" {
			c[pricing.ResourceNvidiaGPU] = q(gpus)
			labels["nvidia.com/gpu.product"] = "Tesla-T4"
		}
		return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Status: corev1.NodeStatus{Capacity: c, Allocatable: c,
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	}
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ml", Name: "infer", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "x", Controller: new(true)}}},
		Spec: corev1.PodSpec{NodeName: "gpu", Containers: []corev1.Container{{Name: "c", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: q("100m"), pricing.ResourceNvidiaGPU: q("1")}, Limits: corev1.ResourceList{pricing.ResourceNvidiaGPU: q("1")}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	rates := pricing.Rates{CPUCoreHour: 0.033, MemoryGBHour: 0.00375, Currency: usd}
	rep := BuildNodes([]corev1.Node{node("a", ""), node("b", ""), node("gpu", "2")}, []corev1.Pod{pod}, nil, rates, time.Now())
	var gpu NodeView
	for _, n := range rep.Nodes {
		if n.Name == "gpu" {
			gpu = n
		}
		if n.ConsolidationCandidate && n.GPUs > 0 {
			t.Errorf("a GPU node is a consolidation candidate: %s", n.Name)
		}
	}
	cpuMem := (8*0.033 + 32*0.00375) * 730
	if gpu.GPUs != 2 || gpu.GPURequested != 1 || gpu.GPUModel != "Tesla-T4" || gpu.GPUHourly != 0.33 || !near(gpu.MonthlyCost, cpuMem+2*0.33*730) {
		t.Errorf("gpu node = %+v", gpu)
	}
	if !near(gpu.MonthlyRequested, 0.1*0.033*730+0.33*730) {
		t.Errorf("requested = %v: the GPU the pod requests is part of it", gpu.MonthlyRequested)
	}
	if rep.Summary.GPUs != 2 || rep.Summary.GPURequested != 1 {
		t.Errorf("summary GPUs = %v/%v", rep.Summary.GPURequested, rep.Summary.GPUs)
	}
}

func TestPodGPURequests(t *testing.T) {
	q := resource.MustParse
	p := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Name: "a", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{pricing.ResourceNvidiaGPU: q("1")}, Limits: corev1.ResourceList{pricing.ResourceNvidiaGPU: q("1")}}},
		{Name: "b", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/mig-1g.5gb": q("2"), corev1.ResourceCPU: q("1")}}},
		{Name: "c", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: q("1")}}},
	}}}
	got := PodGPURequests(p)
	if len(got) != 2 || got.Name(pricing.ResourceNvidiaGPU, resource.DecimalSI).Value() != 1 || got.Name("nvidia.com/mig-1g.5gb", resource.DecimalSI).Value() != 2 {
		t.Errorf("PodGPURequests = %v: requests win over limits, which count when there are none", got)
	}
}
