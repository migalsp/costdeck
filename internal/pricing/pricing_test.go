package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func awsNode(name, instanceType string, cpu, mem string, spot bool) *corev1.Node {
	labels := map[string]string{corev1.LabelTopologyRegion: "eu-central-1", corev1.LabelInstanceTypeStable: instanceType}
	if spot {
		labels["karpenter.sh/capacity-type"] = "spot"
	}
	return &corev1.Node{
		Name: name, Labels: labels,
		Spec: corev1.NodeSpec{ProviderID: "aws:///eu-central-1a/i-" + name},
		Status: corev1.NodeStatus{Capacity: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem),
		}},
	}
}

func resolver(t *testing.T, spec finopsv1.CostDeckConfigSpec, pricer NodePricer, objs ...client.Object) *Resolver {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	objs = append(objs, &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck", Spec: spec})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &Resolver{Client: c, AWS: func(context.Context, *finopsv1.CostDeckConfig) (NodePricer, error) {
		if pricer == nil {
			return nil, errors.New("no credentials")
		}
		return pricer, nil
	}}
}

type fakePricer map[string]float64

func (f fakePricer) HourlyPrice(_ context.Context, region, instanceType string) (float64, error) {
	if p, ok := f[region+"/"+instanceType]; ok {
		return p, nil
	}
	return 0, errors.New("unknown instance type")
}

func TestCustomRatesWin(t *testing.T) {
	r := resolver(t, finopsv1.CostDeckConfigSpec{
		Pricing:  finopsv1.PricingConfig{CPUCoreHour: "0.02", MemoryGBHour: "0.002", Currency: "eur"},
		Features: finopsv1.FeaturesConfig{CloudPricingAPI: true},
	}, fakePricer{}, awsNode("a", "m5.large", "2", "8Gi", false))
	rates := r.Rates(context.Background())
	if rates.CPUCoreHour != 0.02 || rates.MemoryGBHour != 0.002 || rates.Currency != "EUR" || !strings.Contains(rates.Basis, "Custom") {
		t.Errorf("Rates() = %+v", rates)
	}
}

func TestHeuristicRatesNameTheCloud(t *testing.T) {
	rates := resolver(t, finopsv1.CostDeckConfigSpec{}, nil, awsNode("a", "m5.large", "2", "8Gi", false)).Rates(context.Background())
	if rates.Basis != "Heuristic list-price estimate (aws)" || rates.Currency != "USD" {
		t.Errorf("Rates() = %+v", rates)
	}
}

func TestAWSRatesReproduceTheNodeBill(t *testing.T) {
	pricer := fakePricer{"eu-central-1/m5.large": 0.115, "eu-central-1/r5.xlarge": 0.304}
	r := resolver(t, finopsv1.CostDeckConfigSpec{Features: finopsv1.FeaturesConfig{CloudPricingAPI: true}}, pricer,
		awsNode("a", "m5.large", "2", "8Gi", false),
		awsNode("b", "r5.xlarge", "4", "32Gi", true),
	)
	rates := r.Rates(context.Background())
	if !strings.Contains(rates.Basis, "AWS on-demand") || !strings.Contains(rates.Basis, "1 spot node") {
		t.Fatalf("Basis = %q", rates.Basis)
	}
	// Pricing the whole cluster's capacity with the derived rates must give back the bill.
	got := rates.Hourly(resource.MustParse("6"), resource.MustParse("40Gi"))
	if want := 0.115 + 0.304; math.Abs(got-want) > 1e-9 {
		t.Errorf("cluster capacity priced at %.6f/h, want the node bill %.6f/h", got, want)
	}
}

func TestAWSFailureFallsBackAndSaysWhy(t *testing.T) {
	r := resolver(t, finopsv1.CostDeckConfigSpec{Features: finopsv1.FeaturesConfig{CloudPricingAPI: true}}, nil,
		awsNode("a", "m5.large", "2", "8Gi", false))
	rates := r.Rates(context.Background())
	if !strings.HasPrefix(rates.Basis, "Heuristic") || !strings.Contains(rates.Basis, "no credentials") {
		t.Errorf("Basis = %q", rates.Basis)
	}
}

type fakeProducts struct{ docs []string }

func (f fakeProducts) GetProducts(context.Context, *awspricing.GetProductsInput, ...func(*awspricing.Options)) (*awspricing.GetProductsOutput, error) {
	return &awspricing.GetProductsOutput{PriceList: f.docs}, nil
}

func TestAWSPricerParsesPriceList(t *testing.T) {
	doc := `{"product":{"attributes":{"instanceType":"m5.large"}},"terms":{"OnDemand":{"X.JRTCKXETXF":{"priceDimensions":{"X.JRTCKXETXF.6YS6EN2CT7":{"unit":"Hrs","pricePerUnit":{"USD":"0.1150000000"}}}}}}}`
	p := &AWSPricer{API: fakeProducts{docs: []string{doc}}}
	price, err := p.HourlyPrice(context.Background(), "eu-central-1", "m5.large")
	if err != nil || price != 0.115 {
		t.Fatalf("HourlyPrice() = %v, %v", price, err)
	}
	if _, err := (&AWSPricer{API: fakeProducts{}}).HourlyPrice(context.Background(), "eu-central-1", "x"); err == nil {
		t.Error("an empty price list must be an error")
	}
}

func TestAzurePricerTakesLinuxPayAsYouGo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := r.URL.Query().Get("$filter")
		if !strings.Contains(f, "armRegionName eq 'westeurope'") || !strings.Contains(f, "armSkuName eq 'Standard_D4s_v5'") {
			http.Error(w, "unexpected filter "+f, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
			{"retailPrice": 0.384, "unitOfMeasure": "1 Hour", "productName": "Virtual Machines Dsv5 Series Windows", "skuName": "D4s v5", "type": "Consumption"},
			{"retailPrice": 0.0384, "unitOfMeasure": "1 Hour", "productName": "Virtual Machines Dsv5 Series", "skuName": "D4s v5 Spot", "type": "Consumption"},
			{"retailPrice": 0.192, "unitOfMeasure": "1 Hour", "productName": "Virtual Machines Dsv5 Series", "skuName": "D4s v5", "type": "Consumption"},
		}})
	}))
	defer srv.Close()

	p := &AzurePricer{HTTP: srv.Client(), BaseURL: srv.URL}
	price, err := p.HourlyPrice(context.Background(), "westeurope", "Standard_D4s_v5")
	if err != nil || price != 0.192 {
		t.Fatalf("HourlyPrice() = %v, %v; want the Linux pay-as-you-go 0.192", price, err)
	}
}

func TestAzureNodesArePricedFromRetailPrices(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	node := &corev1.Node{
		Name: "aks-1", Labels: map[string]string{corev1.LabelTopologyRegion: "westeurope", corev1.LabelInstanceTypeStable: "Standard_D4s_v5"},
		Spec:   corev1.NodeSpec{ProviderID: "azure:///subscriptions/x/resourceGroups/mc/providers/Microsoft.Compute/virtualMachineScaleSets/aks/virtualMachines/0"},
		Status: corev1.NodeStatus{Capacity: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, &finopsv1.CostDeckConfig{
		Name: "default", Namespace: "costdeck",
		Spec: finopsv1.CostDeckConfigSpec{Features: finopsv1.FeaturesConfig{CloudPricingAPI: true}},
	}).Build()
	r := &Resolver{Client: c, Azure: func(context.Context, *finopsv1.CostDeckConfig) (NodePricer, error) {
		return fakePricer{"westeurope/Standard_D4s_v5": 0.192}, nil
	}}
	rates := r.Rates(context.Background())
	if !strings.Contains(rates.Basis, "Azure pay-as-you-go") {
		t.Fatalf("Basis = %q", rates.Basis)
	}
	if got := rates.Hourly(resource.MustParse("4"), resource.MustParse("16Gi")); math.Abs(got-0.192) > 1e-9 {
		t.Errorf("the node priced at %.4f/h, want its list price 0.192", got)
	}
}

func resolverFor(t *testing.T, cfg *finopsv1.CostDeckConfig, aws NodePricer, objs ...client.Object) *Resolver {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	cfg.Name, cfg.Namespace = "default", "costdeck"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, cfg)...).Build()
	return &Resolver{Client: c, AWS: func(context.Context, *finopsv1.CostDeckConfig) (NodePricer, error) { return aws, nil }}
}

type countingPricer struct {
	prices map[string]float64
	calls  []string
}

func (c *countingPricer) HourlyPrice(_ context.Context, region, instanceType string) (float64, error) {
	c.calls = append(c.calls, region+"/"+instanceType)
	if p, ok := c.prices[region+"/"+instanceType]; ok {
		return p, nil
	}
	return 0, errors.New("unknown instance type")
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestRatesListFactorAndMonthly(t *testing.T) {
	for factor, want := range map[float64]float64{0: 1, -0.5: 1, 0.8: 0.8, 1.2: 1.2} {
		if got := (Rates{Factor: factor}).ListFactor(); got != want {
			t.Errorf("ListFactor() with factor %v = %v, want %v", factor, got, want)
		}
	}
	r := Rates{CPUCoreHour: 0.04, MemoryGBHour: 0.005}
	if got := r.Monthly(resource.MustParse("500m"), resource.MustParse("2Gi")); !near(got, (0.5*0.04+2*0.005)*HoursPerMonth) {
		t.Errorf("Monthly() = %v, want the hourly cost times %d hours", got, HoursPerMonth)
	}
}

func TestBillingFactorScalesListPrices(t *testing.T) {
	a10g := gpuNode("gpu", "g5.xlarge", "4", "16Gi", map[string]string{labelGPUProduct: "NVIDIA-A10G"}, corev1.ResourceList{ResourceNvidiaGPU: resource.MustParse("1")})
	recent := metav1.NewTime(time.Now().Add(-24 * time.Hour))
	billed := func(factor, currency string, at metav1.Time) *finopsv1.BillingStatus {
		return &finopsv1.BillingStatus{Source: "AWS Cost Explorer", Factor: factor, From: "2026-09-01", To: "2026-09-30", Currency: currency, LastReconciled: at}
	}
	tests := []struct {
		name         string
		spec         finopsv1.CostDeckConfigSpec
		status       *finopsv1.BillingStatus
		pricer       NodePricer
		wantCPU      float64
		wantGPU      float64
		wantFactor   float64
		wantCurrency string
		wantBasis    string
	}{
		{
			name: "heuristic rates", spec: finopsv1.CostDeckConfigSpec{Billing: finopsv1.BillingConfig{Enabled: true}},
			status: billed("0.8", "EUR", recent), wantCPU: 0.033 * 0.8, wantGPU: 0.81 * 0.8, wantFactor: 0.8, wantCurrency: "EUR",
			wantBasis: "reconciled with AWS Cost Explorer: billed 80% of list price (2026-09-01 to 2026-09-30)",
		},
		{
			name:   "price list rates",
			spec:   finopsv1.CostDeckConfigSpec{Billing: finopsv1.BillingConfig{Enabled: true}, Features: finopsv1.FeaturesConfig{CloudPricingAPI: true}},
			status: billed("0.8", "", recent), pricer: fakePricer{"eu-central-1/m6i.xlarge": 0.192, "eu-central-1/g5.xlarge": 1.006},
			wantCPU: 0.033 * 0.8, wantGPU: 0.814 * 0.8, wantFactor: 0.8, wantCurrency: "USD", wantBasis: "AWS on-demand list prices",
		},
		{
			name:   "custom rates are what you pay already",
			spec:   finopsv1.CostDeckConfigSpec{Billing: finopsv1.BillingConfig{Enabled: true}, Pricing: finopsv1.PricingConfig{CPUCoreHour: "0.05", MemoryGBHour: "0.005"}},
			status: billed("0.8", "EUR", recent), wantCPU: 0.05, wantGPU: 0.81, wantCurrency: "USD", wantBasis: "Custom rates from CostDeckConfig",
		},
		{
			name: "reconciliation switched off", spec: finopsv1.CostDeckConfigSpec{},
			status: billed("0.8", "EUR", recent), wantCPU: 0.033, wantGPU: 0.81, wantCurrency: "USD", wantBasis: "Heuristic list-price estimate (aws)",
		},
		{
			name: "factor too old", spec: finopsv1.CostDeckConfigSpec{Billing: finopsv1.BillingConfig{Enabled: true}},
			status: billed("0.8", "EUR", metav1.NewTime(time.Now().Add(-9*24*time.Hour))), wantCPU: 0.033, wantGPU: 0.81, wantCurrency: "USD", wantBasis: "Heuristic list-price estimate (aws)",
		},
		{
			name: "factor not a number", spec: finopsv1.CostDeckConfigSpec{Billing: finopsv1.BillingConfig{Enabled: true}},
			status: billed("n/a", "EUR", recent), wantCPU: 0.033, wantGPU: 0.81, wantCurrency: "USD", wantBasis: "Heuristic list-price estimate (aws)",
		},
		{
			name: "factor zero", spec: finopsv1.CostDeckConfigSpec{Billing: finopsv1.BillingConfig{Enabled: true}},
			status: billed("0", "EUR", recent), wantCPU: 0.033, wantGPU: 0.81, wantCurrency: "USD", wantBasis: "Heuristic list-price estimate (aws)",
		},
		{
			name: "never reconciled", spec: finopsv1.CostDeckConfigSpec{Billing: finopsv1.BillingConfig{Enabled: true}},
			wantCPU: 0.033, wantGPU: 0.81, wantCurrency: "USD", wantBasis: "Heuristic list-price estimate (aws)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &finopsv1.CostDeckConfig{Spec: tt.spec}
			cfg.Status.Billing = tt.status
			r := resolverFor(t, cfg, tt.pricer, awsNode("cpu", "m6i.xlarge", "4", "16Gi", false), a10g)
			for round := range 2 {
				rates := r.Rates(context.Background())
				if !near(rates.CPUCoreHour, tt.wantCPU) || !near(rates.NodeGPUHour(*a10g), tt.wantGPU) || !near(rates.GPUHour, tt.wantGPU) {
					t.Errorf("round %d: CPU %v GPU %v (average %v), want CPU %v GPU %v", round, rates.CPUCoreHour, rates.NodeGPUHour(*a10g), rates.GPUHour, tt.wantCPU, tt.wantGPU)
				}
				if rates.Factor != tt.wantFactor || rates.Currency != tt.wantCurrency || !strings.Contains(rates.Basis, tt.wantBasis) {
					t.Errorf("round %d: factor %v currency %q basis %q", round, rates.Factor, rates.Currency, rates.Basis)
				}
				if tt.wantFactor == 0 && strings.Contains(rates.Basis, "reconciled") {
					t.Errorf("round %d: basis claims a reconciliation: %q", round, rates.Basis)
				}
			}
		})
	}
}

func TestResolverCachesPriceListRatesPerNodeSet(t *testing.T) {
	pricer := &countingPricer{prices: map[string]float64{"eu-central-1/m5.large": 0.115, "eu-central-1/r5.xlarge": 0.304}}
	r := resolverFor(t, &finopsv1.CostDeckConfig{Spec: finopsv1.CostDeckConfigSpec{Features: finopsv1.FeaturesConfig{CloudPricingAPI: true}}}, pricer,
		awsNode("a", "m5.large", "2", "8Gi", false), awsNode("b", "m5.large", "2", "8Gi", false))
	ctx := context.Background()

	first := r.Rates(ctx)
	if len(pricer.calls) != 1 {
		t.Fatalf("price list calls = %v, want one per region and instance type", pricer.calls)
	}
	if second := r.Rates(ctx); len(pricer.calls) != 1 || second.CPUCoreHour != first.CPUCoreHour {
		t.Errorf("unchanged nodes were priced again: %v", pricer.calls)
	}

	if err := r.Client.(client.Client).Create(ctx, awsNode("c", "r5.xlarge", "4", "32Gi", false)); err != nil {
		t.Fatal(err)
	}
	if third := r.Rates(ctx); len(pricer.calls) != 3 || !strings.Contains(third.Basis, "3 node(s) (m5.large, r5.xlarge)") {
		t.Errorf("a new instance type must re-price the cluster: calls %v, basis %q", pricer.calls, third.Basis)
	}

	r.TTL = time.Nanosecond
	time.Sleep(time.Millisecond)
	r.Rates(ctx)
	if len(pricer.calls) != 5 {
		t.Errorf("expired rates must be priced again: calls %v", pricer.calls)
	}
}

func TestResolverFallsBackToHeuristics(t *testing.T) {
	gce := &corev1.Node{Name: "gke-1", Spec: corev1.NodeSpec{ProviderID: "gce://proj/europe-west1-b/gke-1"},
		Labels: map[string]string{corev1.LabelTopologyRegion: "europe-west1", corev1.LabelInstanceTypeStable: "e2-standard-4"}}
	unlabelled := awsNode("a", "", "2", "8Gi", false)
	delete(unlabelled.Labels, corev1.LabelInstanceTypeStable)
	tests := []struct {
		name      string
		features  finopsv1.FeaturesConfig
		nodes     []client.Object
		wantCPU   float64
		wantBasis string
		wantCalls int
	}{
		{name: "price list switched off", nodes: []client.Object{awsNode("a", "m5.large", "2", "8Gi", false)}, wantCPU: 0.033, wantBasis: "Heuristic list-price estimate (aws)"},
		{name: "no nodes", features: finopsv1.FeaturesConfig{CloudPricingAPI: true}, wantCPU: 0.031611, wantBasis: "Heuristic list-price estimate"},
		{name: "a cloud without a price list", features: finopsv1.FeaturesConfig{CloudPricingAPI: true}, nodes: []client.Object{gce}, wantCPU: 0.031611, wantBasis: "Heuristic list-price estimate (gcp)"},
		{
			name: "nodes without instance-type labels", features: finopsv1.FeaturesConfig{CloudPricingAPI: true}, nodes: []client.Object{unlabelled}, wantCPU: 0.033,
			wantBasis: "Heuristic list-price estimate (aws)", wantCalls: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricer := &countingPricer{prices: map[string]float64{"eu-central-1/m5.large": 0.115}}
			rates := resolverFor(t, &finopsv1.CostDeckConfig{Spec: finopsv1.CostDeckConfigSpec{Features: tt.features}}, pricer, tt.nodes...).Rates(context.Background())
			if rates.CPUCoreHour != tt.wantCPU || !strings.HasPrefix(rates.Basis, tt.wantBasis) || rates.Currency != "USD" {
				t.Errorf("Rates() = %+v, want CPU %v and basis %q", rates, tt.wantCPU, tt.wantBasis)
			}
			if len(pricer.calls) != tt.wantCalls {
				t.Errorf("price list calls = %v", pricer.calls)
			}
		})
	}

	labels := resolverFor(t, &finopsv1.CostDeckConfig{Spec: finopsv1.CostDeckConfigSpec{Features: finopsv1.FeaturesConfig{CloudPricingAPI: true}}}, &countingPricer{}, unlabelled).Rates(context.Background())
	if !strings.Contains(labels.Basis, "no node carries instance-type and region labels") {
		t.Errorf("basis = %q, want the reason the price list could not be used", labels.Basis)
	}
}

func TestResolverBasisNamesAtMostFourInstanceTypes(t *testing.T) {
	pricer := fakePricer{}
	nodes := make([]client.Object, 0, 6)
	for i, it := range []string{"m5.large", "c5.large", "r5.large", "t3.large", "m6i.large", "c6i.large"} {
		pricer["eu-central-1/"+it] = 0.1
		nodes = append(nodes, awsNode(fmt.Sprintf("n%d", i), it, "2", "8Gi", false))
	}
	rates := resolverFor(t, &finopsv1.CostDeckConfig{Spec: finopsv1.CostDeckConfigSpec{Features: finopsv1.FeaturesConfig{CloudPricingAPI: true}}}, pricer, nodes...).Rates(context.Background())
	if want := "AWS on-demand list prices for 6 node(s) (c5.large, c6i.large, m5.large, m6i.large, +2 more)"; rates.Basis != want {
		t.Errorf("Basis = %q, want %q", rates.Basis, want)
	}
}

func TestInstancePrice(t *testing.T) {
	pricer := fakePricer{"eu-central-1/m5.large": 0.115, "eu-central-1/free.tier": 0, "westeurope/Standard_D4s_v5": 0.192}
	builds := 0
	var buildErr error
	build := func(_ context.Context, _ *finopsv1.CostDeckConfig) (NodePricer, error) {
		builds++
		if buildErr != nil {
			return nil, buildErr
		}
		return pricer, nil
	}
	on := finopsv1.CostDeckConfigSpec{Features: finopsv1.FeaturesConfig{CloudPricingAPI: true}}
	tests := []struct {
		name                    string
		spec                    finopsv1.CostDeckConfigSpec
		cloud, region, instance string
		buildErr                error
		wantPrice               float64
		wantOK                  bool
		wantBuilt               bool
	}{
		{name: "AWS", spec: on, cloud: "aws", region: "eu-central-1", instance: "m5.large", wantPrice: 0.115, wantOK: true, wantBuilt: true},
		{name: "Azure", spec: on, cloud: "azure", region: "westeurope", instance: "Standard_D4s_v5", wantPrice: 0.192, wantOK: true, wantBuilt: true},
		{name: "price list switched off", cloud: "aws", region: "eu-central-1", instance: "m5.large"},
		{name: "no region", spec: on, cloud: "aws", instance: "m5.large"},
		{name: "a cloud without a price list", spec: on, cloud: "gcp", region: "europe-west1", instance: "e2-standard-4"},
		{name: "price list unavailable", spec: on, cloud: "aws", region: "eu-central-1", instance: "m5.large", buildErr: errors.New("no credentials"), wantBuilt: true},
		{name: "unknown instance type", spec: on, cloud: "aws", region: "eu-central-1", instance: "x9.huge", wantBuilt: true},
		{name: "free instance type", spec: on, cloud: "aws", region: "eu-central-1", instance: "free.tier", wantBuilt: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builds, buildErr = 0, tt.buildErr
			r := resolverFor(t, &finopsv1.CostDeckConfig{Spec: tt.spec}, nil)
			r.AWS, r.Azure = build, build
			price, ok := r.InstancePrice(context.Background(), tt.cloud, tt.region, tt.instance)
			if price != tt.wantPrice || ok != tt.wantOK {
				t.Errorf("InstancePrice() = %v, %v; want %v, %v", price, ok, tt.wantPrice, tt.wantOK)
			}
			if (builds > 0) != tt.wantBuilt {
				t.Errorf("price list built %d times", builds)
			}
		})
	}
}

func TestHeuristicRatesAndDetectCloud(t *testing.T) {
	for cloud, want := range map[string]Rates{
		"aws":   {CPUCoreHour: 0.033, MemoryGBHour: 0.00375, Currency: "USD", Basis: "Heuristic list-price estimate (aws)"},
		"azure": {CPUCoreHour: 0.033, MemoryGBHour: 0.00375, Currency: "USD", Basis: "Heuristic list-price estimate (azure)"},
		"gcp":   {CPUCoreHour: 0.031611, MemoryGBHour: 0.004237, Currency: "USD", Basis: "Heuristic list-price estimate (gcp)"},
		"local": {CPUCoreHour: 0.031611, MemoryGBHour: 0.004237, Currency: "USD", Basis: "Heuristic list-price estimate"},
	} {
		got := HeuristicRates(cloud)
		if got.CPUCoreHour != want.CPUCoreHour || got.MemoryGBHour != want.MemoryGBHour || got.Currency != want.Currency || got.Basis != want.Basis {
			t.Errorf("HeuristicRates(%q) = %+v, want %+v", cloud, got, want)
		}
	}

	node := func(providerID string) corev1.Node { return corev1.Node{Spec: corev1.NodeSpec{ProviderID: providerID}} }
	tests := []struct {
		name  string
		nodes []corev1.Node
		want  string
	}{
		{"no nodes", nil, "local"},
		{"kind", []corev1.Node{node("kind://docker/kind/kind-control-plane")}, "local"},
		{"EKS", []corev1.Node{node("aws:///eu-central-1a/i-0abc")}, "aws"},
		{"upper-case scheme", []corev1.Node{node("AWS:///eu-central-1a/i-0abc")}, "aws"},
		{"AKS", []corev1.Node{node("azure:///subscriptions/x/resourceGroups/y")}, "azure"},
		{"GKE", []corev1.Node{node("gce://proj/europe-west1-b/gke-1")}, "gcp"},
		{"first cloud node wins", []corev1.Node{node(""), node("gce://proj/z/n"), node("aws:///z/i-1")}, "gcp"},
	}
	for _, tt := range tests {
		if got := DetectCloud(tt.nodes); got != tt.want {
			t.Errorf("%s: DetectCloud() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestAzureRetailSharesOnePricer(t *testing.T) {
	build := AzureRetail()
	a, errA := build(context.Background(), nil)
	b, errB := build(context.Background(), &finopsv1.CostDeckConfig{})
	if errA != nil || errB != nil || a != b {
		t.Fatalf("AzureRetail() built %p and %p (%v, %v); every caller must share one cached pricer", a, b, errA, errB)
	}
	if p, ok := a.(*AzurePricer); !ok || p.BaseURL != "https://prices.azure.com" || p.HTTP == nil || p.HTTP.Timeout != 20*time.Second {
		t.Errorf("pricer = %#v, want the public Retail Prices endpoint with a timeout", a)
	}
}
