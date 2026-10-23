package pricing

import (
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// GPUs are priced apart from CPU and memory: a GPU node's price is mostly its GPUs, and
// spreading it over cores and GiB would make every pod in the cluster look expensive.

// GPU extended resources and the labels GPU feature discovery puts on nodes.
const (
	ResourceNvidiaGPU    corev1.ResourceName = "nvidia.com/gpu"
	ResourceNvidiaShared corev1.ResourceName = "nvidia.com/gpu.shared"
	ResourceAMDGPU       corev1.ResourceName = "amd.com/gpu"
	nvidiaMIGPrefix                          = "nvidia.com/mig-"
	labelGPUProduct                          = "nvidia.com/gpu.product"
	labelGPUCount                            = "nvidia.com/gpu.count"
	labelGPUReplicas                         = "nvidia.com/gpu.replicas"
	// migSlices is how many compute slices the MIG-capable GPUs (A100, H100, H200) have.
	migSlices = 7
)

// gpuHourly are on-demand prices per GPU-hour by model, worked out from AWS us-east-1 list
// prices (October 2026) of instances with one GPU model, minus their CPU and memory at the
// heuristic rates: g4dn (T4), g6 (L4), g5 (A10G), g6e (L40S), p3 (V100), p4d and p4de
// (A100 40 and 80 GB), p5 (H100), p5en (H200), p6-b200 (B200). They price GPUs where no
// price list is available. The first match wins, so longer names come first.
var gpuHourly = []struct {
	match  string
	hourly float64
}{
	{"B200", 12.49},
	{"H200", 6.16},
	{"H100", 5.13},
	{"A100-SXM4-80GB", 2.50},
	{"A100-80GB", 2.50},
	{"A100", 1.81},
	{"V100", 2.57},
	{"L40S", 1.61},
	{"A10G", 0.81},
	{"L4", 0.61},
	{"T4", 0.33},
}

// unknownGPUHourly prices a GPU whose model nothing names: the A10G, a common middle.
const unknownGPUHourly = 0.81

// HeuristicGPUHour estimates what one GPU of a model costs per hour, from the model name
// GPU feature discovery reports ("NVIDIA-A10G", "Tesla-T4").
func HeuristicGPUHour(model string) float64 {
	upper := strings.ToUpper(model)
	for _, g := range gpuHourly {
		if strings.Contains(upper, g.match) {
			return g.hourly
		}
	}
	return unknownGPUHourly
}

// GPUModel is a node's GPU model as GPU feature discovery labels it, without the -SHARED
// suffix time-slicing adds, or "".
func GPUModel(n corev1.Node) string {
	return strings.TrimSuffix(n.Labels[labelGPUProduct], "-SHARED")
}

// replicas is how many shares time-slicing or MPS makes of each GPU on a node.
func replicas(n *corev1.Node) float64 {
	if n == nil {
		return 1
	}
	if r, err := strconv.ParseFloat(n.Labels[labelGPUReplicas], 64); err == nil && r > 1 {
		return r
	}
	return 1
}

// NodeGPUs is the number of physical GPUs on a node.
func NodeGPUs(n corev1.Node) float64 {
	nvidia := 0.0
	if c, err := strconv.ParseFloat(n.Labels[labelGPUCount], 64); err == nil && c > 0 {
		nvidia = c
	} else {
		capacity := n.Status.Capacity
		for name, q := range capacity {
			if name == ResourceNvidiaGPU || name == ResourceNvidiaShared {
				nvidia += q.AsApproximateFloat64() / replicas(&n)
			}
		}
		if nvidia == 0 {
			nvidia = migGPUs(capacity)
		}
	}
	amd := n.Status.Capacity[ResourceAMDGPU]
	return nvidia + amd.AsApproximateFloat64()
}

// GPUUnits is how many physical GPUs a resource list (a pod's requests) amounts to on a
// node: a time-sliced or MPS share is 1/replicas of a GPU, a MIG device <n>g.<mem> is n of
// its seven compute slices.
func GPUUnits(list corev1.ResourceList, node *corev1.Node) float64 {
	units := 0.0
	for name, q := range list {
		switch name {
		case ResourceNvidiaGPU, ResourceNvidiaShared:
			units += q.AsApproximateFloat64() / replicas(node)
		case ResourceAMDGPU:
			units += q.AsApproximateFloat64()
		}
	}
	return units + migGPUs(list)
}

// migGPUs sums MIG devices (nvidia.com/mig-1g.5gb and the like) as fractions of a GPU.
func migGPUs(list corev1.ResourceList) float64 {
	total := 0.0
	for name, q := range list {
		profile, ok := strings.CutPrefix(string(name), nvidiaMIGPrefix)
		if !ok {
			continue
		}
		slices, _, _ := strings.Cut(profile, "g")
		if n, err := strconv.ParseFloat(slices, 64); err == nil && n > 0 {
			total += q.AsApproximateFloat64() * n / migSlices
		}
	}
	return total
}

// IsGPUNode reports whether a node has GPUs.
func IsGPUNode(n corev1.Node) bool { return NodeGPUs(n) > 0 }

// NodeGPUHour is what one GPU of a node costs per hour: a custom GPU rate, else its share
// of the node's list price, else the estimate for its model.
func (r Rates) NodeGPUHour(n corev1.Node) float64 {
	if r.customGPU > 0 {
		return r.customGPU
	}
	if p, ok := r.gpuByType[n.Labels[corev1.LabelInstanceTypeStable]]; ok {
		return p
	}
	return HeuristicGPUHour(GPUModel(n)) * r.ListFactorOrOne()
}

// ListFactorOrOne is the billing factor estimates are scaled by, 1 when there is none.
func (r Rates) ListFactorOrOne() float64 {
	if r.Factor > 0 {
		return r.Factor
	}
	return 1
}

// NodeGPUsHourly is what all GPUs of a node cost per hour.
func (r Rates) NodeGPUsHourly(n corev1.Node) float64 {
	gpus := NodeGPUs(n)
	if gpus == 0 {
		return 0
	}
	return gpus * r.NodeGPUHour(n)
}
