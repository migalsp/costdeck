package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/pricing/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

const m5LargeProduct = `{"product":{"attributes":{"instanceType":"m5.large"}},"terms":{"OnDemand":{"X.JRTCKXETXF":{"priceDimensions":{"X.JRTCKXETXF.6YS6EN2CT7":{"unit":"Hrs","pricePerUnit":{"USD":"0.1150000000"}}}}}}}`

type priceListAPI struct {
	mu       sync.Mutex
	requests []map[string]any
	targets  []string
	auth     []string
	status   int
	body     string
}

func (f *priceListAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(raw, &req)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.targets = append(f.targets, r.Header.Get("X-Amz-Target"))
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	status, body := f.status, f.body
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"FormatVersion": "aws_v1", "PriceList": []string{m5LargeProduct}})
}

func TestNewAWSPricerAsksThePriceListAPI(t *testing.T) {
	api := &priceListAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()
	pricer := NewAWSPricer(aws.Config{
		Region:           "eu-central-1",
		BaseEndpoint:     aws.String(srv.URL),
		Credentials:      credentials.NewStaticCredentialsProvider("AKIDTEST", "secret", ""),
		RetryMaxAttempts: 1,
	})
	ctx := context.Background()

	price, err := pricer.HourlyPrice(ctx, "eu-central-1", "m5.large")
	if err != nil || price != 0.115 {
		t.Fatalf("HourlyPrice() = %v, %v", price, err)
	}
	if again, err := pricer.HourlyPrice(ctx, "eu-central-1", "m5.large"); err != nil || again != price || len(api.requests) != 1 {
		t.Errorf("a cached price was asked again: %v, %v after %d requests", again, err, len(api.requests))
	}
	if api.targets[0] != "AWSPriceListService.GetProducts" {
		t.Errorf("X-Amz-Target = %q", api.targets[0])
	}
	if !strings.Contains(api.auth[0], "/us-east-1/pricing/aws4_request") {
		t.Errorf("Authorization = %q, want a request signed for the Price List region us-east-1", api.auth[0])
	}
	req := api.requests[0]
	if req["ServiceCode"] != "AmazonEC2" || req["MaxResults"] != float64(10) {
		t.Errorf("request = %v", req)
	}
	filters := map[string]string{}
	for _, f := range req["Filters"].([]any) {
		m := f.(map[string]any)
		if m["Type"] != "TERM_MATCH" {
			t.Errorf("filter %v is not a term match", m)
		}
		filters[m["Field"].(string)] = m["Value"].(string)
	}
	want := map[string]string{"instanceType": "m5.large", "regionCode": "eu-central-1", "operatingSystem": "Linux", "tenancy": "Shared", "preInstalledSw": "NA", "capacitystatus": "Used"}
	for k, v := range want {
		if filters[k] != v {
			t.Errorf("filter %s = %q, want %q (filters %v)", k, filters[k], v, filters)
		}
	}
}

func TestAWSPricerDoesNotCacheFailures(t *testing.T) {
	api := &priceListAPI{status: http.StatusBadRequest, body: `{"__type":"InvalidParameterException","Message":"Input parameters are invalid"}`}
	srv := httptest.NewServer(api)
	defer srv.Close()
	pricer := NewAWSPricer(aws.Config{BaseEndpoint: aws.String(srv.URL), Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1})
	ctx := context.Background()

	if _, err := pricer.HourlyPrice(ctx, "eu-central-1", "m5.large"); err == nil || !strings.Contains(err.Error(), "InvalidParameterException") {
		t.Fatalf("HourlyPrice() error = %v, want the API error", err)
	}
	api.mu.Lock()
	api.status = 0
	api.mu.Unlock()
	if price, err := pricer.HourlyPrice(ctx, "eu-central-1", "m5.large"); err != nil || price != 0.115 || len(api.requests) != 2 {
		t.Errorf("after a failure HourlyPrice() = %v, %v with %d requests; the failure must not be cached", price, err, len(api.requests))
	}
}

type recordingProducts struct {
	inputs []*awspricing.GetProductsInput
	docs   []string
	err    error
}

func (r *recordingProducts) GetProducts(_ context.Context, in *awspricing.GetProductsInput, _ ...func(*awspricing.Options)) (*awspricing.GetProductsOutput, error) {
	r.inputs = append(r.inputs, in)
	if r.err != nil {
		return nil, r.err
	}
	return &awspricing.GetProductsOutput{PriceList: r.docs}, nil
}

func TestAWSPricerCachesPerRegionAndInstanceType(t *testing.T) {
	api := &recordingProducts{docs: []string{m5LargeProduct}}
	p := &AWSPricer{API: api}
	ctx := context.Background()
	for _, q := range [][2]string{{"eu-central-1", "m5.large"}, {"eu-central-1", "m5.large"}, {"us-east-1", "m5.large"}} {
		if _, err := p.HourlyPrice(ctx, q[0], q[1]); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.inputs) != 2 {
		t.Fatalf("GetProducts calls = %d, want one per region and instance type", len(api.inputs))
	}
	region := ""
	for _, f := range api.inputs[1].Filters {
		if aws.ToString(f.Field) == "regionCode" {
			region = aws.ToString(f.Value)
		}
		if f.Type != types.FilterTypeTermMatch {
			t.Errorf("filter %s is %s", aws.ToString(f.Field), f.Type)
		}
	}
	if region != "us-east-1" {
		t.Errorf("second call asked for region %q", region)
	}

	api.err = errors.New("AccessDeniedException: not authorized to perform pricing:GetProducts")
	if _, err := p.HourlyPrice(ctx, "ap-south-1", "m5.large"); err == nil || !strings.Contains(err.Error(), "pricing:GetProducts") {
		t.Errorf("HourlyPrice() error = %v, want the API error passed on", err)
	}
}

func TestOnDemandUSD(t *testing.T) {
	product := func(unit, usd string) string {
		return `{"terms":{"OnDemand":{"T":{"priceDimensions":{"D":{"unit":"` + unit + `","pricePerUnit":{"USD":"` + usd + `"}}}}}}}`
	}
	tests := []struct {
		name    string
		docs    []string
		want    float64
		wantErr string
	}{
		{name: "no products", wantErr: "the Price List API has no matching product"},
		{name: "first hourly price", docs: []string{product("Hrs", "0.096")}, want: 0.096},
		{name: "broken documents are skipped", docs: []string{"{not json", product("Hrs", "0.2")}, want: 0.2},
		{name: "other units are skipped", docs: []string{product("Quantity", "500"), product("Hrs", "0.3")}, want: 0.3},
		{name: "free dimensions are skipped", docs: []string{product("Hrs", "0.0000000000"), product("Hrs", "0.4")}, want: 0.4},
		{name: "no hourly USD price", docs: []string{product("Quantity", "1"), `{"terms":{"OnDemand":{"T":{"priceDimensions":{"D":{"unit":"Hrs","pricePerUnit":{"CNY":"1.2"}}}}}}}`}, wantErr: "no hourly on-demand USD price in 2 product(s)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := onDemandUSD(tt.docs)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("onDemandUSD() = %v, %v; want error %q", got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("onDemandUSD() = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestAWSFromConfigCachesPerSecretAndRegion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{Name: "aws", Namespace: "costdeck", Data: map[string][]byte{"AWS_ACCESS_KEY_ID": []byte("AKID"), "AWS_SECRET_ACCESS_KEY": []byte("secret")}},
		&corev1.Secret{Name: "partial", Namespace: "costdeck", Data: map[string][]byte{"AWS_ACCESS_KEY_ID": []byte("AKID")}},
	).Build()
	build := AWSFromConfig(c)
	ctx := context.Background()
	withAWS := func(secretRef, region string) *finopsv1.CostDeckConfig {
		cfg := &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck"}
		cfg.Spec.Providers.AWS = &finopsv1.AWSProviderConfig{SecretRef: secretRef, Region: region}
		return cfg
	}

	first, err := build(ctx, withAWS("aws", "eu-central-1"))
	if err != nil {
		t.Fatal(err)
	}
	if region := first.(*AWSPricer).API.(*awspricing.Client).Options().Region; region != "us-east-1" {
		t.Errorf("the Price List client uses region %q, want us-east-1 where the API lives", region)
	}
	if again, err := build(ctx, withAWS("aws", "eu-central-1")); err != nil || again != first {
		t.Errorf("the same credentials built a new pricer (%p, %p, %v); its price cache would be lost", first, again, err)
	}
	if other, err := build(ctx, withAWS("aws", "us-west-2")); err != nil || other == first {
		t.Errorf("a new region reused the old pricer (%p, %v)", other, err)
	}

	if _, err := build(ctx, withAWS("missing", "")); err == nil || !strings.Contains(err.Error(), "failed to read secret costdeck/missing") {
		t.Errorf("a missing Secret = %v", err)
	}
	if _, err := build(ctx, withAWS("partial", "")); err == nil || !strings.Contains(err.Error(), "missing AWS_ACCESS_KEY_ID or AWS_SECRET_ACCESS_KEY") {
		t.Errorf("a Secret without keys = %v", err)
	}

	identity, err := build(ctx, &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck"})
	if err != nil {
		t.Fatalf("without stored credentials the pod identity is used: %v", err)
	}
	if again, _ := build(ctx, &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck"}); again != identity {
		t.Error("the pod-identity pricer was not reused")
	}
}
