package pricing

import (
	"fmt"
	"strconv"
	"strings"
)

// Volumes and load balancers are billed apart from the nodes. These are list prices in USD
// (us-east-1, eastus, us-central1) per GiB-month and per load balancer-month; custom rates
// in the CostDeckConfig replace them.

// diskPrices maps a cloud's disk type to its price per GiB-month.
var diskPrices = map[string]map[string]float64{
	"aws": {
		"gp3": 0.08, "gp2": 0.10, "io1": 0.125, "io2": 0.125, "st1": 0.045, "sc1": 0.015, "standard": 0.05,
		"efs": 0.30,
	},
	"azure": {
		"premium_lrs": 0.15, "premium_zrs": 0.19, "premiumv2_lrs": 0.09, "standardssd_lrs": 0.075, "standardssd_zrs": 0.09,
		"standard_lrs": 0.045, "ultrassd_lrs": 0.12, "files": 0.06,
	},
	"gcp": {
		"pd-standard": 0.04, "pd-balanced": 0.10, "pd-ssd": 0.17, "pd-extreme": 0.125,
		"hyperdisk-balanced": 0.08, "hyperdisk-throughput": 0.05, "filestore": 0.20,
	},
}

// estimatedGiBMonth prices a network-attached volume on a cluster without a cloud price
// list (OpenStack Cinder, Ceph, vSphere, …) like a general-purpose cloud SSD: AWS gp3 costs
// $0.08 a GiB-month, Azure Standard SSD $0.075 and Google Cloud pd-balanced $0.10.
const estimatedGiBMonth = 0.08

// defaultDisk is each cloud's default disk type when a class names none.
var defaultDisk = map[string]string{"aws": "gp3", "azure": "standardssd_lrs", "gcp": "pd-balanced"}

// loadBalancerMonth is the base price of one cloud load balancer per month, before
// traffic: AWS NLB/ALB and Azure and Google Cloud forwarding rules all cost about this.
var loadBalancerMonth = map[string]float64{"aws": 16.43, "azure": 18.25, "gcp": 18.25}

// localProvisioners keep data on the node's own disk, which the node price already covers.
var localProvisioners = []string{"local-path", "rancher.io", "kubernetes.io/no-provisioner", "hostpath", "openebs.io/local", "topolvm"}

// StorageClassInfo is what pricing needs from a StorageClass.
type StorageClassInfo struct {
	Provisioner string
	Parameters  map[string]string
}

// VolumePrice is the monthly price of a volume per GiB and what it is based on.
type VolumePrice struct {
	PerGiBMonth float64
	// Type is the disk type, e.g. gp3 or Premium_LRS.
	Type string
	// Local is true for volumes on the node's own disk, already paid for with the node.
	Local bool
	Basis string
}

// Custom rates for storage and load balancers, from the CostDeckConfig.
type InfraRates struct {
	StorageGiBMonth   string
	LoadBalancerMonth string
	Currency          string
}

func parseRate(v string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return f, err == nil && f >= 0 && strings.TrimSpace(v) != ""
}

// PriceVolume prices a volume of the given class on the given cloud (aws, azure, gcp or
// local). Without list prices in the cost currency it returns zero and says why.
func PriceVolume(cloud string, class StorageClassInfo, custom InfraRates) VolumePrice {
	for _, p := range localProvisioners {
		if strings.Contains(class.Provisioner, p) {
			return VolumePrice{Local: true, Type: "local", Basis: "on the node's own disk, included in the node price"}
		}
	}
	diskType := strings.ToLower(firstNonEmpty(class.Parameters, "type", "skuname", "skuName", "storageaccounttype", "storageAccountType"))
	switch {
	case strings.Contains(class.Provisioner, "efs"):
		diskType = "efs"
	case strings.Contains(class.Provisioner, "file.csi.azure") || strings.Contains(class.Provisioner, "azure-file"):
		diskType = "files"
	case strings.Contains(class.Provisioner, "filestore"):
		diskType = "filestore"
	}
	if diskType == "" {
		diskType = defaultDisk[cloud]
	}
	if rate, ok := parseRate(custom.StorageGiBMonth); ok {
		return VolumePrice{PerGiBMonth: rate, Type: diskType, Basis: "custom storage rate"}
	}
	if custom.Currency != "" && !strings.EqualFold(custom.Currency, "USD") {
		return VolumePrice{Type: diskType, Basis: fmt.Sprintf("no storage rate in %s; set one under Settings → Prices", custom.Currency)}
	}
	if price, ok := diskPrices[cloud][diskType]; ok {
		return VolumePrice{PerGiBMonth: price, Type: diskType, Basis: fmt.Sprintf("%s %s list price", cloudNames[cloud], diskType)}
	}
	if def, ok := diskPrices[cloud][defaultDisk[cloud]]; ok {
		return VolumePrice{PerGiBMonth: def, Type: diskType, Basis: fmt.Sprintf("unknown disk type %q, priced as %s", diskType, defaultDisk[cloud])}
	}
	if _, known := diskPrices[cloud]; !known {
		return VolumePrice{PerGiBMonth: estimatedGiBMonth, Type: diskType,
			Basis: "estimate: priced like a general-purpose cloud SSD; set your own rate under Settings → Prices"}
	}
	return VolumePrice{Type: diskType, Basis: "no list price for this storage; set a storage rate under Settings → Prices"}
}

// PriceLoadBalancer returns the monthly base price of one load balancer.
func PriceLoadBalancer(cloud string, custom InfraRates) (float64, string) {
	if rate, ok := parseRate(custom.LoadBalancerMonth); ok {
		return rate, "custom load balancer rate"
	}
	if custom.Currency != "" && !strings.EqualFold(custom.Currency, "USD") {
		return 0, fmt.Sprintf("no load balancer rate in %s", custom.Currency)
	}
	if p, ok := loadBalancerMonth[cloud]; ok {
		return p, fmt.Sprintf("%s load balancer list price, before traffic", cloudNames[cloud])
	}
	return 0, "no cloud load balancer price: in-cluster ones such as MetalLB cost nothing extra; set a rate under Settings → Prices if yours do"
}

func firstNonEmpty(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}
