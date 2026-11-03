package pricing

import (
	"strings"
	"testing"
)

func TestPriceVolume(t *testing.T) {
	ebs := "ebs.csi.aws.com"
	azureDisk := "disk.csi.azure.com"
	pd := "pd.csi.storage.gke.io"
	tests := []struct {
		name      string
		cloud     string
		class     StorageClassInfo
		custom    InfraRates
		want      VolumePrice
		wantBasis string
	}{
		{
			name: "local-path on any cloud", cloud: "aws", class: StorageClassInfo{Provisioner: "rancher.io/local-path"},
			want: VolumePrice{Local: true, Type: "local"}, wantBasis: "included in the node price",
		},
		{
			name: "OpenEBS local volumes", cloud: "local", class: StorageClassInfo{Provisioner: "openebs.io/local"},
			want: VolumePrice{Local: true, Type: "local"}, wantBasis: "on the node's own disk",
		},
		{
			name: "local rates win over a custom rate", cloud: "aws", class: StorageClassInfo{Provisioner: "kubernetes.io/no-provisioner"}, custom: InfraRates{StorageGiBMonth: "0.5"},
			want: VolumePrice{Local: true, Type: "local"}, wantBasis: "on the node's own disk",
		},
		{
			name: "EBS gp2", cloud: "aws", class: StorageClassInfo{Provisioner: ebs, Parameters: map[string]string{"type": "gp2"}},
			want: VolumePrice{PerGiBMonth: 0.10, Type: "gp2"}, wantBasis: "AWS gp2 list price",
		},
		{
			name: "EBS without a type is gp3", cloud: "aws", class: StorageClassInfo{Provisioner: ebs},
			want: VolumePrice{PerGiBMonth: 0.08, Type: "gp3"}, wantBasis: "AWS gp3 list price",
		},
		{
			name: "EFS whatever the parameters say", cloud: "aws", class: StorageClassInfo{Provisioner: "efs.csi.aws.com", Parameters: map[string]string{"type": "gp3"}},
			want: VolumePrice{PerGiBMonth: 0.30, Type: "efs"}, wantBasis: "AWS efs list price",
		},
		{
			name: "Azure disk SKU in mixed case", cloud: "azure", class: StorageClassInfo{Provisioner: azureDisk, Parameters: map[string]string{"skuName": "Premium_LRS"}},
			want: VolumePrice{PerGiBMonth: 0.15, Type: "premium_lrs"}, wantBasis: "Azure premium_lrs list price",
		},
		{
			name: "legacy Azure storageaccounttype", cloud: "azure", class: StorageClassInfo{Provisioner: "kubernetes.io/azure-disk", Parameters: map[string]string{"storageaccounttype": "Standard_LRS"}},
			want: VolumePrice{PerGiBMonth: 0.045, Type: "standard_lrs"}, wantBasis: "Azure standard_lrs list price",
		},
		{
			name: "type beats skuName", cloud: "azure", class: StorageClassInfo{Provisioner: azureDisk, Parameters: map[string]string{"type": "UltraSSD_LRS", "skuName": "Premium_LRS"}},
			want: VolumePrice{PerGiBMonth: 0.12, Type: "ultrassd_lrs"}, wantBasis: "Azure ultrassd_lrs list price",
		},
		{
			name: "Azure Files", cloud: "azure", class: StorageClassInfo{Provisioner: "file.csi.azure.com", Parameters: map[string]string{"skuName": "Premium_LRS"}},
			want: VolumePrice{PerGiBMonth: 0.06, Type: "files"}, wantBasis: "Azure files list price",
		},
		{
			name: "Filestore", cloud: "gcp", class: StorageClassInfo{Provisioner: "filestore.csi.storage.gke.io"},
			want: VolumePrice{PerGiBMonth: 0.20, Type: "filestore"}, wantBasis: "Google Cloud filestore list price",
		},
		{
			name: "unknown disk type on a known cloud", cloud: "gcp", class: StorageClassInfo{Provisioner: pd, Parameters: map[string]string{"type": "pd-future"}},
			want: VolumePrice{PerGiBMonth: 0.10, Type: "pd-future"}, wantBasis: `unknown disk type "pd-future", priced as pd-balanced`,
		},
		{
			name: "cluster without a cloud", cloud: "local", class: StorageClassInfo{Provisioner: "cinder.csi.openstack.org"},
			want: VolumePrice{PerGiBMonth: 0.08}, wantBasis: "estimate: priced like a general-purpose cloud SSD",
		},
		{
			name: "custom rate", cloud: "aws", class: StorageClassInfo{Provisioner: ebs, Parameters: map[string]string{"type": "io2"}}, custom: InfraRates{StorageGiBMonth: " 0.05 ", Currency: "EUR"},
			want: VolumePrice{PerGiBMonth: 0.05, Type: "io2"}, wantBasis: "custom storage rate",
		},
		{
			name: "custom rate of zero", cloud: "aws", class: StorageClassInfo{Provisioner: ebs}, custom: InfraRates{StorageGiBMonth: "0"},
			want: VolumePrice{Type: "gp3"}, wantBasis: "custom storage rate",
		},
		{
			name: "invalid custom rate falls back to the list price", cloud: "aws", class: StorageClassInfo{Provisioner: ebs}, custom: InfraRates{StorageGiBMonth: "-1"},
			want: VolumePrice{PerGiBMonth: 0.08, Type: "gp3"}, wantBasis: "AWS gp3 list price",
		},
		{
			name: "no USD list price in another currency", cloud: "aws", class: StorageClassInfo{Provisioner: ebs}, custom: InfraRates{Currency: "EUR"},
			want: VolumePrice{Type: "gp3"}, wantBasis: "no storage rate in EUR",
		},
		{
			name: "USD in lower case keeps list prices", cloud: "aws", class: StorageClassInfo{Provisioner: ebs}, custom: InfraRates{Currency: "usd"},
			want: VolumePrice{PerGiBMonth: 0.08, Type: "gp3"}, wantBasis: "AWS gp3 list price",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PriceVolume(tt.cloud, tt.class, tt.custom)
			if got.PerGiBMonth != tt.want.PerGiBMonth || got.Type != tt.want.Type || got.Local != tt.want.Local {
				t.Errorf("PriceVolume() = %+v, want %+v", got, tt.want)
			}
			if !strings.Contains(got.Basis, tt.wantBasis) {
				t.Errorf("basis = %q, want it to contain %q", got.Basis, tt.wantBasis)
			}
		})
	}
}

func TestPriceLoadBalancer(t *testing.T) {
	tests := []struct {
		name      string
		cloud     string
		custom    InfraRates
		want      float64
		wantBasis string
	}{
		{"AWS", "aws", InfraRates{}, 16.43, "AWS load balancer list price, before traffic"},
		{"Azure", "azure", InfraRates{}, 18.25, "Azure load balancer list price"},
		{"Google Cloud", "gcp", InfraRates{}, 18.25, "Google Cloud load balancer list price"},
		{"MetalLB and friends", "local", InfraRates{}, 0, "in-cluster ones such as MetalLB cost nothing extra"},
		{"custom rate", "aws", InfraRates{LoadBalancerMonth: "25", Currency: "CHF"}, 25, "custom load balancer rate"},
		{"no USD list price in another currency", "aws", InfraRates{Currency: "CHF"}, 0, "no load balancer rate in CHF"},
		{"unparseable custom rate", "gcp", InfraRates{LoadBalancerMonth: "twenty"}, 18.25, "Google Cloud load balancer list price"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, basis := PriceLoadBalancer(tt.cloud, tt.custom)
			if got != tt.want || !strings.Contains(basis, tt.wantBasis) {
				t.Errorf("PriceLoadBalancer() = %v, %q; want %v and %q", got, basis, tt.want, tt.wantBasis)
			}
		})
	}
}
