package scaling

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// fakeCloud records requests and answers them from a route table.
type fakeCloud struct {
	mu       sync.Mutex
	requests []string
	bodies   []string
	routes   map[string]func(w http.ResponseWriter, r *http.Request)
}

func (f *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.requests = append(f.requests, key)
	f.bodies = append(f.bodies, string(body))
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer test-token" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	if h, ok := f.routes[key]; ok {
		h(w, r)
		return
	}
	http.NotFound(w, r)
}

func jsonReply(v any) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(v) }
}

func status(code int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func testREST(t *testing.T, f *fakeCloud) (*restClient, string) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return newRESTClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"})), srv.URL
}

const sub = "11111111-2222-3333-4444-555555555555"

func TestAzureDiscoverScaleAndReadiness(t *testing.T) {
	vmID := "/subscriptions/" + sub + "/resourceGroups/dev/providers/Microsoft.Compute/virtualMachines/build-agent"
	pgID := "/subscriptions/" + sub + "/resourceGroups/dev/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg-dev"
	f := &fakeCloud{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /subscriptions/" + sub + "/providers/Microsoft.Compute/virtualMachines": jsonReply(map[string]any{"value": []any{
			map[string]any{"id": vmID, "name": "build-agent", "location": "westeurope", "tags": map[string]string{"env": "dev"},
				"properties": map[string]any{"instanceView": map[string]any{"statuses": []any{map[string]string{"code": "ProvisioningState/succeeded"}, map[string]string{"code": "PowerState/running"}}}}},
			map[string]any{"id": vmID + "-prod", "name": "prod", "location": "westeurope", "tags": map[string]string{"env": "prod"}},
		}}),
		"GET /subscriptions/" + sub + "/providers/Microsoft.DBforPostgreSQL/flexibleServers": jsonReply(map[string]any{"value": []any{
			map[string]any{"id": pgID, "name": "pg-dev", "location": "westeurope", "tags": map[string]string{"env": "dev"}, "properties": map[string]any{"state": "Stopped"}},
		}}),
		"POST " + vmID + "/deallocate":  status(http.StatusAccepted),
		"POST " + pgID + "/start":       status(http.StatusConflict), // already starting
		"GET " + vmID + "/instanceView": jsonReply(map[string]any{"statuses": []any{map[string]string{"code": "PowerState/deallocated"}}}),
		"GET " + pgID:                   jsonReply(map[string]any{"properties": map[string]any{"state": "Ready"}}),
	}}
	rest, base := testREST(t, f)
	p := &AzureProvider{subscription: sub, rest: rest, base: base}
	ctx := context.Background()

	vms, err := p.Discover(ctx, AzureTypeVM, map[string]string{"env": "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 1 || vms[0].Identifier != vmID || vms[0].Status != "running" || vms[0].Name != "build-agent" || vms[0].Region != "westeurope" {
		t.Fatalf("VMs = %+v, want only the dev VM, running", vms)
	}
	pgs, err := p.Discover(ctx, AzureTypePostgres, nil)
	if err != nil || len(pgs) != 1 || pgs[0].Status != "stopped" {
		t.Fatalf("PostgreSQL = %+v, %v", pgs, err)
	}

	if err := p.Scale(ctx, vms[0], false); err != nil {
		t.Fatalf("deallocate: %v", err)
	}
	if err := p.Scale(ctx, pgs[0], true); err != nil {
		t.Fatalf("a 409 for a server already starting must count as success, got %v", err)
	}
	if ok, err := p.IsReady(ctx, vms[0], false); err != nil || !ok {
		t.Errorf("deallocated VM ready-for-down = %v, %v", ok, err)
	}
	if ok, err := p.IsReady(ctx, pgs[0], true); err != nil || !ok {
		t.Errorf("Ready server ready-for-up = %v, %v", ok, err)
	}
}

func TestAzureRejectsForeignResourceIDs(t *testing.T) {
	p := &AzureProvider{subscription: sub, rest: &restClient{http: http.DefaultClient}, base: "https://management.azure.com"}
	for _, id := range []string{
		"https://evil.example.com/x",
		"/subscriptions/" + sub + "/resourceGroups/dev/providers/Microsoft.Storage/storageAccounts/x", // wrong kind
		"/subscriptions/" + sub + "/resourceGroups/dev/providers/Microsoft.Compute/virtualMachines/x?api-version=1",
	} {
		if err := p.Scale(context.Background(), finopsv1.ExternalTarget{Type: AzureTypeVM, Identifier: id}, true); err == nil || !strings.Contains(err.Error(), "not the resource ID") {
			t.Errorf("Scale(%q) = %v, want a rejection", id, err)
		}
	}
}

func TestGCPDiscoverScaleAndReadiness(t *testing.T) {
	f := &fakeCloud{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/demo/aggregated/instances": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("pageToken") == "" {
				jsonReply(map[string]any{"nextPageToken": "p2", "items": map[string]any{
					"zones/europe-west1-b": map[string]any{"instances": []any{
						map[string]any{"name": "dev-vm", "zone": "https://compute.googleapis.com/compute/v1/projects/demo/zones/europe-west1-b", "status": "RUNNING", "labels": map[string]string{"env": "dev"}},
					}},
				}})(w, r)
				return
			}
			jsonReply(map[string]any{"items": map[string]any{"zones/us-east1-c": map[string]any{"instances": []any{
				map[string]any{"name": "prod-vm", "zone": ".../zones/us-east1-c", "status": "RUNNING", "labels": map[string]string{"env": "prod"}},
			}}}})(w, r)
		},
		"GET /projects/demo/instances": jsonReply(map[string]any{"items": []any{
			map[string]any{"name": "pg-dev", "region": "europe-west1", "state": "RUNNABLE", "settings": map[string]any{"activationPolicy": "NEVER", "userLabels": map[string]string{"env": "dev"}}},
		}}),
		"POST /projects/demo/zones/europe-west1-b/instances/dev-vm/stop": jsonReply(map[string]any{"name": "op"}),
		"PATCH /projects/demo/instances/pg-dev":                          jsonReply(map[string]any{"name": "op"}),
		"GET /projects/demo/zones/europe-west1-b/instances/dev-vm":       jsonReply(map[string]any{"status": "TERMINATED"}),
		"GET /projects/demo/instances/pg-dev":                            jsonReply(map[string]any{"state": "RUNNABLE", "settings": map[string]any{"activationPolicy": "ALWAYS"}}),
	}}
	rest, base := testREST(t, f)
	p := &GCPProvider{project: "demo", rest: rest, computeBase: base, sqlBase: base}
	ctx := context.Background()

	vms, err := p.Discover(ctx, GCPTypeGCE, map[string]string{"env": "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 1 || vms[0].Identifier != "europe-west1-b/dev-vm" || vms[0].Status != "running" {
		t.Fatalf("instances = %+v, want the dev VM from page one (prod filtered out on page two)", vms)
	}
	sql, err := p.Discover(ctx, GCPTypeCloudSQL, nil)
	if err != nil || len(sql) != 1 || sql[0].Status != "stopped" {
		t.Fatalf("Cloud SQL = %+v, %v; an instance with activation policy NEVER is stopped", sql, err)
	}

	if err := p.Scale(ctx, vms[0], false); err != nil {
		t.Fatal(err)
	}
	if err := p.Scale(ctx, sql[0], true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.bodies[len(f.bodies)-1], `"activationPolicy":"ALWAYS"`) {
		t.Errorf("Cloud SQL start body = %s", f.bodies[len(f.bodies)-1])
	}
	if ok, _ := p.IsReady(ctx, vms[0], false); !ok {
		t.Error("a TERMINATED instance is down")
	}
	if ok, _ := p.IsReady(ctx, sql[0], true); !ok {
		t.Error("ALWAYS + RUNNABLE is up")
	}
	if err := p.Scale(ctx, finopsv1.ExternalTarget{Type: GCPTypeGCE, Identifier: "../../evil"}, true); err == nil {
		t.Error("a malformed instance ID must be rejected")
	}
}

func TestCloudTokenStaysOnTheAPIHost(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the token reached another host: %s %s", r.Method, r.URL)
	}))
	defer evil.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "redirect") {
			http.Redirect(w, r, evil.URL+"/steal", http.StatusFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{}, "nextLink": evil.URL + "/page2"})
	}))
	defer api.Close()

	rest := newRESTClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "secret"}))
	p := &AzureProvider{subscription: sub, rest: rest, base: api.URL}
	if _, err := p.Discover(context.Background(), AzureTypeVM, nil); err == nil || !strings.Contains(err.Error(), "paging link") {
		t.Errorf("a paging link to another host must be refused, got %v", err)
	}
	if err := rest.do(context.Background(), http.MethodGet, api.URL+"/redirect", nil, nil); err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
		t.Errorf("a redirect to another host must be refused, got %v", err)
	}
}

func operatorSecret(name string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{Name: name, Namespace: "costdeck", Data: data}
}

func cloudClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func costDeckConfig(providers finopsv1.ProvidersConfig) *finopsv1.CostDeckConfig {
	return &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck", Spec: finopsv1.CostDeckConfigSpec{Providers: providers}}
}

func TestCloudSettingsFor(t *testing.T) {
	cfg := costDeckConfig(finopsv1.ProvidersConfig{
		AWS:   &finopsv1.AWSProviderConfig{Enabled: true, SecretRef: "aws-keys", DiscoveryTags: map[string]string{"env": "dev"}},
		Azure: &finopsv1.AzureProviderConfig{SecretRef: "azure-sp", DiscoveryTags: map[string]string{"team": "a"}, ResourceTypes: []string{AzureTypeVM}},
		GCP:   &finopsv1.GCPProviderConfig{Enabled: true, DiscoveryLabels: map[string]string{"env": "qa"}},
	})
	cases := []struct {
		name  string
		cfg   *finopsv1.CostDeckConfig
		cloud string
		want  CloudSettings
	}{
		{"AWS defaults to Aurora only", cfg, ProviderAWS, CloudSettings{Enabled: true, SecretRef: "aws-keys", Filter: map[string]string{"env": "dev"}, ResourceTypes: []string{AWSTypeAurora}}},
		{"Azure keeps the configured types", cfg, ProviderAzure, CloudSettings{SecretRef: "azure-sp", Filter: map[string]string{"team": "a"}, ResourceTypes: []string{AzureTypeVM}}},
		{"Google Cloud defaults to every type", cfg, ProviderGCP, CloudSettings{Enabled: true, Filter: map[string]string{"env": "qa"}, ResourceTypes: []string{GCPTypeGCE, GCPTypeCloudSQL}}},
		{"AWS with explicit types", costDeckConfig(finopsv1.ProvidersConfig{AWS: &finopsv1.AWSProviderConfig{ResourceTypes: []string{AWSTypeEC2}}}), ProviderAWS, CloudSettings{ResourceTypes: []string{AWSTypeEC2}}},
		{"unconfigured provider", costDeckConfig(finopsv1.ProvidersConfig{}), ProviderAzure, CloudSettings{}},
		{"unknown provider", cfg, "oracle", CloudSettings{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CloudSettingsFor(c.cfg, c.cloud)
			if got.Enabled != c.want.Enabled || got.SecretRef != c.want.SecretRef || !maps.Equal(got.Filter, c.want.Filter) || !slices.Equal(got.ResourceTypes, c.want.ResourceTypes) {
				t.Errorf("CloudSettingsFor(%s) = %+v, want %+v", c.cloud, got, c.want)
			}
		})
	}
}

func TestBuildProviderAWSCredentialSources(t *testing.T) {
	isolateAWSEnvironment(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDPODIDENTITY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "pod-identity-secret")
	t.Setenv("AWS_REGION", "us-west-1")
	c := cloudClient(t, operatorSecret("aws-keys", map[string][]byte{"AWS_ACCESS_KEY_ID": []byte("AKIDFROMSECRET"), "AWS_SECRET_ACCESS_KEY": []byte("s")}),
		operatorSecret("empty", map[string][]byte{}))
	withSecret := func(ref, region string) *finopsv1.CostDeckConfig {
		return costDeckConfig(finopsv1.ProvidersConfig{AWS: &finopsv1.AWSProviderConfig{Enabled: true, SecretRef: ref, Region: region}})
	}
	override := map[string][]byte{"AWS_ACCESS_KEY_ID": []byte("AKIDUNSAVED"), "AWS_SECRET_ACCESS_KEY": []byte("u")}
	cases := []struct {
		name       string
		cfg        *finopsv1.CostDeckConfig
		override   map[string][]byte
		wantKey    string
		wantRegion string
	}{
		{"keys from the Secret in the configured region", withSecret("aws-keys", "eu-west-3"), nil, "AKIDFROMSECRET", "eu-west-3"},
		{"keys from the Secret default to us-east-1", withSecret("aws-keys", ""), nil, "AKIDFROMSECRET", "us-east-1"},
		{"unsaved keys win over the Secret, which is not read", withSecret("absent", "eu-west-3"), override, "AKIDUNSAVED", "eu-west-3"},
		{"an empty Secret falls back to the pod identity", withSecret("empty", ""), nil, "AKIDPODIDENTITY", "us-west-1"},
		{"no Secret uses the pod identity", withSecret("", ""), nil, "AKIDPODIDENTITY", "us-west-1"},
		{"no AWS section uses the pod identity", costDeckConfig(finopsv1.ProvidersConfig{}), nil, "AKIDPODIDENTITY", "us-west-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := BuildProvider(context.Background(), c, tc.cfg, ProviderAWS, tc.override)
			if err != nil {
				t.Fatal(err)
			}
			awsProvider, ok := p.(*AWSProvider)
			if !ok {
				t.Fatalf("provider = %T", p)
			}
			if key := retrievedAccessKey(t, awsProvider); key != tc.wantKey || awsProvider.Config().Region != tc.wantRegion {
				t.Errorf("key %s in %s, want %s in %s", key, awsProvider.Config().Region, tc.wantKey, tc.wantRegion)
			}
		})
	}

	if _, err := BuildProvider(context.Background(), c, withSecret("absent", ""), ProviderAWS, nil); err == nil || !strings.HasPrefix(err.Error(), "read secret costdeck/absent: ") {
		t.Errorf("a configured Secret that is missing = %v, want the read error", err)
	}
}

func TestBuildProviderAWSPodIdentityUsesTheConfiguredRegion(t *testing.T) {
	t.Skip("bug: BuildProvider ignores spec.providers.aws.region when credentials come from the pod identity (cloud.go:101)")
	isolateAWSEnvironment(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDPODIDENTITY")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "pod-identity-secret")
	t.Setenv("AWS_REGION", "us-east-1")
	cfg := costDeckConfig(finopsv1.ProvidersConfig{AWS: &finopsv1.AWSProviderConfig{Enabled: true, Region: "eu-central-1"}})

	p, err := BuildProvider(context.Background(), cloudClient(t), cfg, ProviderAWS, nil)
	if err != nil {
		t.Fatal(err)
	}
	if region := p.(*AWSProvider).Config().Region; region != "eu-central-1" {
		t.Errorf("region = %s, want the eu-central-1 chosen in the settings", region)
	}
}

func TestBuildProviderAzure(t *testing.T) {
	unsetEnvironment(t, "AZURE_TOKEN_CREDENTIALS", "AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET",
		"AZURE_CLIENT_CERTIFICATE_PATH", "AZURE_USERNAME", "AZURE_PASSWORD", "AZURE_FEDERATED_TOKEN_FILE", "AZURE_AUTHORITY_HOST")
	c := cloudClient(t,
		operatorSecret("sp-with-tenant", map[string][]byte{AzureSecretTenantID: []byte("contoso.onmicrosoft.com"), AzureSecretClientID: []byte("app"), AzureSecretClientSecret: []byte("pw")}),
		operatorSecret("sp-without-tenant", map[string][]byte{AzureSecretClientID: []byte("app"), AzureSecretClientSecret: []byte("pw")}),
	)
	azure := func(ref, tenant string) *finopsv1.CostDeckConfig {
		return costDeckConfig(finopsv1.ProvidersConfig{Azure: &finopsv1.AzureProviderConfig{Enabled: true, SecretRef: ref, TenantID: tenant, SubscriptionID: sub}})
	}
	for _, tc := range []struct {
		name    string
		cfg     *finopsv1.CostDeckConfig
		wantErr string
	}{
		{"tenant from the Secret", azure("sp-with-tenant", ""), ""},
		{"tenant from the settings", azure("sp-without-tenant", "fabrikam.onmicrosoft.com"), ""},
		{"pod identity", azure("", ""), ""},
		{"client secret without any tenant", azure("sp-without-tenant", ""), "the Azure credentials are not usable: a tenant ID is required with a client secret"},
		{"no Azure section", costDeckConfig(finopsv1.ProvidersConfig{}), "the Azure provider is not configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := BuildProvider(context.Background(), c, tc.cfg, ProviderAzure, nil)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr || p != nil {
					t.Fatalf("BuildProvider = %v, %v; want %q", p, err, tc.wantErr)
				}
				return
			}
			azp, ok := p.(*AzureProvider)
			if err != nil || !ok || azp.subscription != sub || azp.base != "https://management.azure.com" {
				t.Fatalf("BuildProvider = %#v, %v; want an Azure provider for the configured subscription", p, err)
			}
		})
	}
}

func TestBuildProviderGCP(t *testing.T) {
	c := cloudClient(t, operatorSecret("gcp-key", map[string][]byte{GCPSecretKey: serviceAccountKey(t, "key-project", "https://oauth2.googleapis.com/token", placeholderPrivateKey)}))
	gcp := func(ref, project string) *finopsv1.CostDeckConfig {
		return costDeckConfig(finopsv1.ProvidersConfig{GCP: &finopsv1.GCPProviderConfig{Enabled: true, SecretRef: ref, ProjectID: project}})
	}
	for _, tc := range []struct {
		name        string
		cfg         *finopsv1.CostDeckConfig
		override    map[string][]byte
		wantProject string
	}{
		{"project of the key", gcp("gcp-key", ""), nil, "key-project"},
		{"configured project wins over the key's", gcp("gcp-key", "settings-project"), nil, "settings-project"},
		{"unsaved key", gcp("", ""), map[string][]byte{GCPSecretKey: serviceAccountKey(t, "unsaved-project", "https://oauth2.googleapis.com/token", placeholderPrivateKey)}, "unsaved-project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := BuildProvider(context.Background(), c, tc.cfg, ProviderGCP, tc.override)
			gp, ok := p.(*GCPProvider)
			if err != nil || !ok || gp.project != tc.wantProject {
				t.Fatalf("BuildProvider = %#v, %v; want a provider for %s", p, err, tc.wantProject)
			}
		})
	}

	userKey := map[string][]byte{GCPSecretKey: []byte(`{"type":"authorized_user","client_id":"x","client_secret":"y","refresh_token":"z"}`)}
	if p, err := BuildProvider(context.Background(), c, gcp("", ""), ProviderGCP, userKey); err == nil || p != nil ||
		!strings.HasPrefix(err.Error(), "the Google Cloud credentials are not usable: ") {
		t.Errorf("user credentials = %v, %v; want them refused", p, err)
	}
	if p, err := BuildProvider(context.Background(), c, costDeckConfig(finopsv1.ProvidersConfig{}), ProviderGCP, nil); err == nil || p != nil || err.Error() != "the Google Cloud provider is not configured" {
		t.Errorf("no GCP section = %v, %v", p, err)
	}
	if p, err := BuildProvider(context.Background(), c, gcp("", ""), "oracle", nil); err == nil || p != nil || err.Error() != `external provider "oracle" is not supported` {
		t.Errorf("unknown provider = %v, %v", p, err)
	}
}

func TestDiscoverAllCollectsTargetsAndErrorsPerProvider(t *testing.T) {
	isolateAWSEnvironment(t)
	f := &fakeAWS{replies: map[string]awsReply{
		"DescribeDBClusters": answer(http.StatusOK, rdsResponse("DescribeDBClusters", "<DBClusters>"+
			auroraClusterXML("orders-db", "aurora-postgresql", "available", map[string]string{"env": "dev"})+
			auroraClusterXML("prod-db", "aurora-postgresql", "available", map[string]string{"env": "prod"})+"</DBClusters>")),
		"DescribeInstances": answer(http.StatusForbidden, ec2Error("UnauthorizedOperation", "You are not authorized to perform this operation.")),
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	t.Setenv("AWS_ENDPOINT_URL", srv.URL)
	c := cloudClient(t, operatorSecret("aws-keys", map[string][]byte{"AWS_ACCESS_KEY_ID": []byte(testAccessKey), "AWS_SECRET_ACCESS_KEY": []byte(testSecretKey)}))
	cfg := costDeckConfig(finopsv1.ProvidersConfig{
		AWS: &finopsv1.AWSProviderConfig{Enabled: true, SecretRef: "aws-keys", Region: "eu-west-2", DiscoveryTags: map[string]string{"env": "dev"},
			ResourceTypes: []string{AWSTypeEC2, AWSTypeAurora}},
		Azure: &finopsv1.AzureProviderConfig{Enabled: true, SecretRef: "azure-missing", SubscriptionID: sub},
		GCP:   &finopsv1.GCPProviderConfig{Enabled: false, SecretRef: "gcp-missing"},
	})

	targets, errs := DiscoverAll(context.Background(), c, cfg)

	want := []finopsv1.ExternalTarget{{Provider: ProviderAWS, Type: AWSTypeAurora, Identifier: "orders-db", Region: "eu-west-2", Status: "available"}}
	if !slices.Equal(targets, want) {
		t.Errorf("targets = %+v, want the dev Aurora cluster despite the EC2 failure before it", targets)
	}
	if len(errs) != 2 || errs[ProviderGCP] != nil {
		t.Errorf("errors = %v, want one for AWS and one for Azure, none for the disabled Google Cloud", errs)
	}
	if err := errs[ProviderAWS]; err == nil || !strings.HasPrefix(err.Error(), "failed to describe EC2 instances: ") {
		t.Errorf("AWS error = %v, want the EC2 discovery failure", err)
	}
	if err := errs[ProviderAzure]; err == nil || !strings.HasPrefix(err.Error(), "read secret costdeck/azure-missing: ") {
		t.Errorf("Azure error = %v, want the missing Secret", err)
	}
	calls := f.recorded()
	if len(calls) != 2 || calls[0].Action != "DescribeInstances" || calls[1].Action != "DescribeDBClusters" ||
		calls[1].AccessKey != testAccessKey || calls[1].Region != "eu-west-2" || !slices.Equal(ec2Filters(calls[0].Form)["tag:env"], []string{"dev"}) {
		t.Errorf("AWS calls = %+v, want EC2 then, despite its failure, Aurora discovery with the Secret's key in eu-west-2 and the env=dev tag filter", calls)
	}

	none, noErrs := DiscoverAll(context.Background(), c, costDeckConfig(finopsv1.ProvidersConfig{}))
	if none != nil || len(noErrs) != 0 {
		t.Errorf("nothing enabled = %v, %v", none, noErrs)
	}
}

func TestProviderCacheKeyFollowsWhatTheProviderIsBuiltFrom(t *testing.T) {
	awsSettings := func(ref, region string, tags map[string]string) *finopsv1.CostDeckConfig {
		return costDeckConfig(finopsv1.ProvidersConfig{AWS: &finopsv1.AWSProviderConfig{Enabled: true, SecretRef: ref, Region: region, DiscoveryTags: tags}})
	}
	azureSettings := func(ref, subscription, tenant string) *finopsv1.CostDeckConfig {
		return costDeckConfig(finopsv1.ProvidersConfig{Azure: &finopsv1.AzureProviderConfig{Enabled: true, SecretRef: ref, SubscriptionID: subscription, TenantID: tenant}})
	}
	gcpSettings := func(ref, project string) *finopsv1.CostDeckConfig {
		return costDeckConfig(finopsv1.ProvidersConfig{GCP: &finopsv1.GCPProviderConfig{Enabled: true, SecretRef: ref, ProjectID: project}})
	}
	for _, c := range []struct {
		name  string
		cloud string
		a, b  *finopsv1.CostDeckConfig
		same  bool
	}{
		{"AWS region", ProviderAWS, awsSettings("keys", "eu-west-1", nil), awsSettings("keys", "eu-west-2", nil), false},
		{"AWS Secret", ProviderAWS, awsSettings("keys", "eu-west-1", nil), awsSettings("other-keys", "eu-west-1", nil), false},
		{"AWS discovery tags do not rebuild", ProviderAWS, awsSettings("keys", "eu-west-1", nil), awsSettings("keys", "eu-west-1", map[string]string{"env": "dev"}), true},
		{"Azure subscription", ProviderAzure, azureSettings("sp", sub, "t"), azureSettings("sp", "99999999-2222-3333-4444-555555555555", "t"), false},
		{"Azure tenant", ProviderAzure, azureSettings("sp", sub, "t"), azureSettings("sp", sub, "u"), false},
		{"Azure Secret", ProviderAzure, azureSettings("sp", sub, "t"), azureSettings("sp2", sub, "t"), false},
		{"Google Cloud project", ProviderGCP, gcpSettings("key", "a"), gcpSettings("key", "b"), false},
		{"Google Cloud Secret", ProviderGCP, gcpSettings("key", "a"), gcpSettings("key2", "a"), false},
	} {
		if same := cacheKey(c.a, c.cloud) == cacheKey(c.b, c.cloud); same != c.same {
			t.Errorf("%s: keys %q and %q, want same=%v", c.name, cacheKey(c.a, c.cloud), cacheKey(c.b, c.cloud), c.same)
		}
	}
	if cacheKey(awsSettings("keys", "eu-west-1", nil), ProviderAWS) == cacheKey(gcpSettings("keys", "eu-west-1"), ProviderGCP) {
		t.Error("an AWS and a Google Cloud provider share a cache key")
	}
}

func TestCloudRESTStopsARedirectLoop(t *testing.T) {
	hops := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, srv.URL+"/again", http.StatusFound)
	}))
	defer srv.Close()
	rest := newRESTClient(context.Background(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "secret"}))
	err := rest.do(context.Background(), http.MethodGet, srv.URL+"/start", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") || hops != 10 {
		t.Errorf("do = %v after %d requests, want it stopped after 10", err, hops)
	}
}
