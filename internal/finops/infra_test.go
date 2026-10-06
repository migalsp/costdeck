package finops

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/migalsp/costdeck-operator/internal/pricing"
)

func claim(ns, name, class, size string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &class,
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
}

func TestBuildInfra(t *testing.T) {
	ready, notReady := true, false
	c := testClient(
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "gp3", Annotations: map[string]string{defaultClassAnnotation: "true"}},
			Provisioner: "ebs.csi.aws.com", Parameters: map[string]string{"type": "gp3"}},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "local"}, Provisioner: "rancher.io/local-path"},
		claim("shop", "db", "gp3", "100Gi"),
		claim("shop", "old", "gp3", "50Gi"),
		claim("dev", "cache", "gp3", "10Gi"),
		claim("shop", "scratch", "local", "20Gi"),
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-left"},
			Spec:       corev1.PersistentVolumeSpec{StorageClassName: "gp3", Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("200Gi")}},
			Status:     corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
		},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Ports: []corev1.ServicePort{{Port: 443}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "legacy"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "web"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "internal"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP}},
		&discoveryv1.EndpointSlice{
			ObjectMeta:  metav1.ObjectMeta{Namespace: "shop", Name: "web-1", Labels: map[string]string{discoveryv1.LabelServiceName: "web"}},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints: []discoveryv1.Endpoint{
				{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
				{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &notReady}},
			},
		},
	)
	pods := []corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db-0"},
		Spec:       corev1.PodSpec{Volumes: []corev1.Volume{{VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "db"}}}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}}
	inf, err := buildInfra(context.Background(), c, c, "aws", pricing.InfraRates{}, pods, map[string]bool{"dev": true})
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]Volume{}
	for _, v := range inf.Volumes {
		state[v.Namespace+"/"+v.Claim+v.Volume] = v
	}
	if v := state["shop/db"]; v.State != StateInUse || !near(v.MonthlyCost, 8) || v.MountedBy[0] != "db-0" {
		t.Errorf("db: %+v", v)
	}
	if v := state["shop/old"]; v.State != StateUnused || !near(v.MonthlyCost, 4) {
		t.Errorf("old should be unused: %+v", v)
	}
	if v := state["dev/cache"]; v.State != StateScaledDown {
		t.Errorf("a claim in a scaled-down namespace is expected to be unmounted: %+v", v)
	}
	if v := state["shop/scratch"]; !v.Local || v.MonthlyCost != 0 {
		t.Errorf("local volumes are part of the node: %+v", v)
	}
	if v := state["/pv-left"]; v.State != StateOrphaned || !near(v.MonthlyCost, 16) {
		t.Errorf("a released volume is orphaned and billed: %+v", v)
	}
	if !near(inf.StorageMonthly, 8+4+0.8+16) || !near(inf.UnusedStorageMonthly, 20) {
		t.Errorf("storage totals: %v unused %v", inf.StorageMonthly, inf.UnusedStorageMonthly)
	}
	lbs := map[string]LoadBalancer{}
	for _, lb := range inf.LoadBalancers {
		lbs[lb.Namespace+"/"+lb.Name] = lb
	}
	if len(lbs) != 3 || lbs["shop/web"].State != StateServing || lbs["shop/web"].ReadyEndpoints != 1 ||
		lbs["shop/legacy"].State != StateIdle || lbs["dev/web"].State != StateScaledDown {
		t.Errorf("load balancers: %+v", inf.LoadBalancers)
	}
	if !near(inf.NetworkMonthly, 3*16.43) || !near(inf.IdleNetworkMonthly, 2*16.43) {
		t.Errorf("network totals: %v idle %v", inf.NetworkMonthly, inf.IdleNetworkMonthly)
	}

	o := NewOverview(Snapshot{Rates: pricing.Rates{Currency: "USD"}, Infra: inf}, nil, nil, 0)
	kinds := map[string]int{}
	for _, op := range o.Opportunities {
		kinds[op.Kind]++
	}
	if kinds[OpportunityUnusedVolumes] != 1 || kinds[OpportunityOrphanVolumes] != 1 || kinds[OpportunityIdleLB] != 2 {
		t.Errorf("infra opportunities: %+v", o.Opportunities)
	}
}

func TestPriceVolume(t *testing.T) {
	cases := []struct {
		cloud string
		class pricing.StorageClassInfo
		rates pricing.InfraRates
		want  float64
		local bool
	}{
		{"aws", pricing.StorageClassInfo{Provisioner: "ebs.csi.aws.com", Parameters: map[string]string{"type": "io2"}}, pricing.InfraRates{}, 0.125, false},
		{"aws", pricing.StorageClassInfo{Provisioner: "kubernetes.io/aws-ebs"}, pricing.InfraRates{}, 0.08, false},
		{"azure", pricing.StorageClassInfo{Provisioner: "disk.csi.azure.com", Parameters: map[string]string{"skuName": "Premium_LRS"}}, pricing.InfraRates{}, 0.15, false},
		{"gcp", pricing.StorageClassInfo{Provisioner: "pd.csi.storage.gke.io", Parameters: map[string]string{"type": "pd-ssd"}}, pricing.InfraRates{}, 0.17, false},
		{"local", pricing.StorageClassInfo{Provisioner: "rancher.io/local-path"}, pricing.InfraRates{}, 0, true},
		{"aws", pricing.StorageClassInfo{Provisioner: "ebs.csi.aws.com"}, pricing.InfraRates{StorageGiBMonth: "0.05"}, 0.05, false},
		// No list price in another currency without a custom rate.
		{"aws", pricing.StorageClassInfo{Provisioner: "ebs.csi.aws.com"}, pricing.InfraRates{Currency: "EUR"}, 0, false},
	}
	for _, c := range cases {
		got := pricing.PriceVolume(c.cloud, c.class, c.rates)
		if !near(got.PerGiBMonth, c.want) || got.Local != c.local {
			t.Errorf("PriceVolume(%s, %+v, %+v) = %+v, want %v", c.cloud, c.class, c.rates, got, c.want)
		}
	}
}
