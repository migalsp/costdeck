package pricing

import (
	"context"
	"maps"
	"math"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func gpuNode(name, instanceType, cpu, mem string, labels map[string]string, capacity corev1.ResourceList) *corev1.Node {
	n := awsNode(name, instanceType, cpu, mem, false)
	maps.Copy(n.Labels, labels)
	maps.Copy(n.Status.Capacity, capacity)
	return n
}

func TestHeuristicGPUHour(t *testing.T) {
	cases := map[string]float64{
		"NVIDIA-A100-SXM4-80GB": 2.50, "NVIDIA-A100-SXM4-40GB": 1.81, "NVIDIA-A100-PCIE-40GB": 1.81,
		"NVIDIA-H100-80GB-HBM3": 5.13, "NVIDIA-H200": 6.16, "NVIDIA-B200": 12.49,
		"NVIDIA-L40S": 1.61, "NVIDIA-L4": 0.61, "NVIDIA-A10G": 0.81, "Tesla-T4": 0.33, "Tesla-V100-SXM2-16GB": 2.57,
		"": unknownGPUHourly, "Some-Future-GPU": unknownGPUHourly,
	}
	for model, want := range cases {
		if got := HeuristicGPUHour(model); got != want {
			t.Errorf("HeuristicGPUHour(%q) = %v, want %v", model, got, want)
		}
	}
	n := corev1.Node{Labels: map[string]string{labelGPUProduct: "NVIDIA-A10G-SHARED"}}
	if GPUModel(n) != "NVIDIA-A10G" {
		t.Errorf("the time-slicing suffix stays: %q", GPUModel(n))
	}
}

func TestGPUCounts(t *testing.T) {
	q := resource.MustParse
	node := func(labels map[string]string, capacity corev1.ResourceList) corev1.Node {
		return corev1.Node{Labels: labels, Status: corev1.NodeStatus{Capacity: capacity}}
	}
	plain := node(nil, corev1.ResourceList{ResourceNvidiaGPU: q("4")})
	sliced := node(map[string]string{labelGPUReplicas: "4"}, corev1.ResourceList{ResourceNvidiaGPU: q("8")})
	labelled := node(map[string]string{labelGPUCount: "2", labelGPUReplicas: "4"}, corev1.ResourceList{ResourceNvidiaGPU: q("8")})
	mig := node(nil, corev1.ResourceList{"nvidia.com/mig-1g.5gb": q("7"), "nvidia.com/mig-3g.20gb": q("2"), corev1.ResourceCPU: q("8")})
	amd := node(nil, corev1.ResourceList{ResourceAMDGPU: q("2")})
	none := node(nil, corev1.ResourceList{corev1.ResourceCPU: q("8")})
	for name, c := range map[string]struct {
		n    corev1.Node
		want float64
	}{"plain": {plain, 4}, "time-sliced": {sliced, 2}, "labelled": {labelled, 2}, "MIG": {mig, 1 + 6.0/7}, "AMD": {amd, 2}, "none": {none, 0}} {
		if got := NodeGPUs(c.n); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: NodeGPUs = %v, want %v", name, got, c.want)
		}
		if IsGPUNode(c.n) != (c.want > 0) {
			t.Errorf("%s: IsGPUNode wrong", name)
		}
	}
	if got := GPUUnits(corev1.ResourceList{ResourceNvidiaGPU: q("1")}, &sliced); got != 0.25 {
		t.Errorf("a time-sliced share = %v, want a quarter GPU", got)
	}
	if got := GPUUnits(corev1.ResourceList{"nvidia.com/mig-3g.20gb": q("1"), corev1.ResourceCPU: q("2")}, &mig); math.Abs(got-3.0/7) > 1e-9 {
		t.Errorf("a 3g MIG device = %v, want 3/7 GPU", got)
	}
	if got := GPUUnits(corev1.ResourceList{ResourceAMDGPU: q("1"), ResourceNvidiaGPU: q("2")}, nil); got != 3 {
		t.Errorf("whole GPUs = %v", got)
	}
}

// A GPU node's price is mostly its GPUs. Spread over cores and GiB, it made every pod in
// the cluster look several times more expensive.
func TestGPUNodesDoNotInflateCPUAndMemoryRates(t *testing.T) {
	pricer := fakePricer{"eu-central-1/m6i.xlarge": 0.192, "eu-central-1/g5.xlarge": 1.006}
	g5 := gpuNode("gpu", "g5.xlarge", "4", "16Gi", map[string]string{labelGPUProduct: "NVIDIA-A10G"}, corev1.ResourceList{ResourceNvidiaGPU: resource.MustParse("1")})
	r := resolver(t, finopsv1.CostDeckConfigSpec{Features: finopsv1.FeaturesConfig{CloudPricingAPI: true}}, pricer, awsNode("cpu", "m6i.xlarge", "4", "16Gi", false), g5)
	rates := r.Rates(context.Background())
	if math.Abs(rates.CPUCoreHour-0.033) > 1e-9 || math.Abs(rates.MemoryGBHour-0.00375) > 1e-9 {
		t.Errorf("CPU and memory rates = %v, %v: the GPU node inflated them", rates.CPUCoreHour, rates.MemoryGBHour)
	}
	if got := rates.NodeGPUHour(*g5); math.Abs(got-0.814) > 1e-9 {
		t.Errorf("the A10G costs %v/h, want the g5's price less its CPU and memory (0.814)", got)
	}
	// The nodes' capacity and GPUs still add up to the bill.
	bill := rates.Hourly(resource.MustParse("8"), resource.MustParse("32Gi")) + rates.NodeGPUsHourly(*g5)
	if math.Abs(bill-(0.192+1.006)) > 1e-9 {
		t.Errorf("priced at %v/h, want the node bill %v/h", bill, 0.192+1.006)
	}
	if math.Abs(rates.GPUHour-0.814) > 1e-9 || !strings.Contains(rates.Basis, "GPUs priced apart on 1 node") {
		t.Errorf("GPUHour %v, basis %q", rates.GPUHour, rates.Basis)
	}
}

func TestGPUOnlyClustersAndCustomGPURates(t *testing.T) {
	p4d := gpuNode("p4d", "p4d.24xlarge", "96", "1152Gi", map[string]string{labelGPUProduct: "NVIDIA-A100-SXM4-40GB"}, corev1.ResourceList{ResourceNvidiaGPU: resource.MustParse("8")})
	r := resolver(t, finopsv1.CostDeckConfigSpec{Features: finopsv1.FeaturesConfig{CloudPricingAPI: true}}, fakePricer{"eu-central-1/p4d.24xlarge": 21.9576}, p4d)
	rates := r.Rates(context.Background())
	if rates.CPUCoreHour != 0.033 || math.Abs(rates.NodeGPUHour(*p4d)-(21.9576-96*0.033-1152*0.00375)/8) > 1e-9 {
		t.Errorf("GPU-only cluster: CPU %v, GPU %v", rates.CPUCoreHour, rates.NodeGPUHour(*p4d))
	}

	// Without a price list the model decides; a custom rate beats both.
	heuristic := resolver(t, finopsv1.CostDeckConfigSpec{}, nil, p4d).Rates(context.Background())
	if heuristic.NodeGPUHour(*p4d) != 1.81 || heuristic.GPUHour != 1.81 {
		t.Errorf("heuristic A100 = %v (average %v)", heuristic.NodeGPUHour(*p4d), heuristic.GPUHour)
	}
	custom := resolver(t, finopsv1.CostDeckConfigSpec{Pricing: finopsv1.PricingConfig{GPUHour: "1.5"}}, nil, p4d).Rates(context.Background())
	if custom.NodeGPUHour(*p4d) != 1.5 || custom.GPUHour != 1.5 || custom.NodeGPUsHourly(*p4d) != 12 {
		t.Errorf("custom GPU rate = %v", custom.NodeGPUHour(*p4d))
	}
	withCPU := resolver(t, finopsv1.CostDeckConfigSpec{Pricing: finopsv1.PricingConfig{CPUCoreHour: "0.02", MemoryGBHour: "0.002", GPUHour: "2"}}, nil, p4d).Rates(context.Background())
	if withCPU.CPUCoreHour != 0.02 || withCPU.NodeGPUHour(*p4d) != 2 {
		t.Errorf("custom CPU and GPU rates = %+v", withCPU)
	}
	if none := resolver(t, finopsv1.CostDeckConfigSpec{}, nil, awsNode("a", "m5.large", "2", "8Gi", false)).Rates(context.Background()); none.GPUHour != 0 {
		t.Errorf("a cluster without GPUs has a GPU rate: %v", none.GPUHour)
	}
}
