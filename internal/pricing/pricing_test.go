package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec:       corev1.NodeSpec{ProviderID: "aws:///eu-central-1a/i-" + name},
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
	objs = append(objs, &finopsv1.CostDeckConfig{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"}, Spec: spec})
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
		ObjectMeta: metav1.ObjectMeta{Name: "aks-1", Labels: map[string]string{corev1.LabelTopologyRegion: "westeurope", corev1.LabelInstanceTypeStable: "Standard_D4s_v5"}},
		Spec:       corev1.NodeSpec{ProviderID: "azure:///subscriptions/x/resourceGroups/mc/providers/Microsoft.Compute/virtualMachineScaleSets/aks/virtualMachines/0"},
		Status:     corev1.NodeStatus{Capacity: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, &finopsv1.CostDeckConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
		Spec:       finopsv1.CostDeckConfigSpec{Features: finopsv1.FeaturesConfig{CloudPricingAPI: true}},
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
