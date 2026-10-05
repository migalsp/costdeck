package finops

import "strings"

// InstanceType is one node shape with its on-demand Linux list price per hour in USD, in
// the reference region of its cloud: AWS us-east-1, Azure eastus, Google Cloud us-central1.
// Recommendations scale these by the ratio of the live price to the catalog price of the
// current type when cloud list prices are on, so other regions compare fairly.
type InstanceType struct {
	Cloud     string
	Name      string
	Family    string // general, compute, memory or burstable
	Arch      string // amd64 or arm64
	VCPU      float64
	MemoryGiB float64
	Hourly    float64
}

func it(cloud, name, family, arch string, vcpu, mem, hourly float64) InstanceType {
	return InstanceType{Cloud: cloud, Name: name, Family: family, Arch: arch, VCPU: vcpu, MemoryGiB: mem, Hourly: hourly}
}

// sizes expands one family from its 2-vCPU size (with mem GiB at hourly) across sizes
// that double in vCPU, memory and price.
func sizes(cloud, family, arch string, names []string, mem, hourly float64) []InstanceType {
	out := make([]InstanceType, 0, len(names))
	for i, n := range names {
		f := float64(int(1) << i)
		out = append(out, it(cloud, n, family, arch, 2*f, mem*f, hourly*f))
	}
	return out
}

func awsSizes(prefix, family, arch string, mem, hourly float64) []InstanceType {
	names := []string{prefix + ".large", prefix + ".xlarge", prefix + ".2xlarge", prefix + ".4xlarge"}
	return sizes("aws", family, arch, names, mem, hourly)
}

func azureSizes(pattern, family, arch string, mem, hourly float64) []InstanceType {
	names := make([]string, 0, 4)
	for _, n := range []string{"2", "4", "8", "16"} {
		names = append(names, strings.Replace(pattern, "#", n, 1))
	}
	return sizes("azure", family, arch, names, mem, hourly)
}

func gcpSizes(prefix, family, arch string, mem, hourly float64) []InstanceType {
	names := []string{prefix + "-2", prefix + "-4", prefix + "-8", prefix + "-16"}
	return sizes("gcp", family, arch, names, mem, hourly)
}

// catalog lists common Kubernetes node shapes. Prices are approximate list prices.
var catalog = func() []InstanceType {
	var c []InstanceType
	add := func(t ...InstanceType) { c = append(c, t...) }
	// AWS
	add(awsSizes("m5", "general", "amd64", 8, 0.096)...)
	add(awsSizes("m6i", "general", "amd64", 8, 0.096)...)
	add(awsSizes("m7i", "general", "amd64", 8, 0.1008)...)
	add(awsSizes("m6a", "general", "amd64", 8, 0.0864)...)
	add(awsSizes("m7a", "general", "amd64", 8, 0.11592)...)
	add(awsSizes("m6g", "general", "arm64", 8, 0.077)...)
	add(awsSizes("m7g", "general", "arm64", 8, 0.0816)...)
	add(awsSizes("c5", "compute", "amd64", 4, 0.085)...)
	add(awsSizes("c6i", "compute", "amd64", 4, 0.085)...)
	add(awsSizes("c7i", "compute", "amd64", 4, 0.08925)...)
	add(awsSizes("c6a", "compute", "amd64", 4, 0.0765)...)
	add(awsSizes("c7g", "compute", "arm64", 4, 0.0725)...)
	add(awsSizes("r5", "memory", "amd64", 16, 0.126)...)
	add(awsSizes("r6i", "memory", "amd64", 16, 0.126)...)
	add(awsSizes("r7i", "memory", "amd64", 16, 0.1323)...)
	add(awsSizes("r6a", "memory", "amd64", 16, 0.1134)...)
	add(awsSizes("r7g", "memory", "arm64", 16, 0.1071)...)
	add(it("aws", "t3.medium", "burstable", "amd64", 2, 4, 0.0416), it("aws", "t3.large", "burstable", "amd64", 2, 8, 0.0832),
		it("aws", "t3.xlarge", "burstable", "amd64", 4, 16, 0.1664), it("aws", "t3.2xlarge", "burstable", "amd64", 8, 32, 0.3328))
	// Azure
	add(azureSizes("Standard_D#s_v5", "general", "amd64", 8, 0.096)...)
	add(azureSizes("Standard_D#as_v5", "general", "amd64", 8, 0.086)...)
	add(azureSizes("Standard_D#ps_v5", "general", "arm64", 8, 0.077)...)
	add(azureSizes("Standard_D#s_v3", "general", "amd64", 8, 0.096)...)
	add(azureSizes("Standard_D#ds_v5", "general", "amd64", 8, 0.113)...)
	add(azureSizes("Standard_E#s_v5", "memory", "amd64", 16, 0.126)...)
	add(azureSizes("Standard_E#as_v5", "memory", "amd64", 16, 0.113)...)
	add(azureSizes("Standard_E#ps_v5", "memory", "arm64", 16, 0.101)...)
	add(azureSizes("Standard_F#s_v2", "compute", "amd64", 4, 0.0846)...)
	add(it("azure", "Standard_B2ms", "burstable", "amd64", 2, 8, 0.0832), it("azure", "Standard_B4ms", "burstable", "amd64", 4, 16, 0.166),
		it("azure", "Standard_DS2_v2", "general", "amd64", 2, 7, 0.146), it("azure", "Standard_DS3_v2", "general", "amd64", 4, 14, 0.293))
	// Google Cloud
	add(gcpSizes("e2-standard", "general", "amd64", 8, 0.067)...)
	add(gcpSizes("e2-highmem", "memory", "amd64", 16, 0.0904)...)
	add(gcpSizes("e2-highcpu", "compute", "amd64", 2, 0.0495)...)
	add(gcpSizes("n2-standard", "general", "amd64", 8, 0.0971)...)
	add(gcpSizes("n2-highmem", "memory", "amd64", 16, 0.131)...)
	add(gcpSizes("n2-highcpu", "compute", "amd64", 2, 0.0717)...)
	add(gcpSizes("n2d-standard", "general", "amd64", 8, 0.0845)...)
	add(gcpSizes("n1-standard", "general", "amd64", 7.5, 0.095)...)
	add(gcpSizes("t2a-standard", "general", "arm64", 8, 0.077)...)
	add(gcpSizes("c4a-standard", "general", "arm64", 8, 0.0859)...)
	return c
}()

// LookupInstanceType finds a node shape in the catalog.
func LookupInstanceType(cloud, name string) (InstanceType, bool) {
	for _, t := range catalog {
		if t.Cloud == cloud && strings.EqualFold(t.Name, name) {
			return t, true
		}
	}
	return InstanceType{}, false
}

// allocatable estimates what pods can request on a node of this shape, using the kubelet
// reservations GKE publishes (EKS and AKS reserve about the same) and a 100 MiB eviction
// threshold.
func (t InstanceType) allocatable() (cpu, memGiB float64) {
	cpuTiers := []tier{{1, 0.06}, {2, 0.01}, {4, 0.005}, {1 << 20, 0.0025}}
	memTiers := []tier{{4, 0.25}, {8, 0.20}, {16, 0.10}, {128, 0.06}, {1 << 20, 0.02}}
	return t.VCPU - tiered(t.VCPU, cpuTiers), t.MemoryGiB - tiered(t.MemoryGiB, memTiers) - 0.1
}

type tier struct{ upTo, share float64 }

// tiered sums share × the part of amount that falls in each tier.
func tiered(amount float64, tiers []tier) float64 {
	total, low := 0.0, 0.0
	for _, t := range tiers {
		if amount <= low {
			break
		}
		total += (min(amount, t.upTo) - low) * t.share
		low = t.upTo
	}
	return total
}
