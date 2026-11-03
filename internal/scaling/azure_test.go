package scaling

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

type fakeAzureCredential struct {
	mu     sync.Mutex
	scopes [][]string
	err    error
}

func (c *fakeAzureCredential) GetToken(_ context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scopes = append(c.scopes, opts.Scopes)
	if c.err != nil {
		return azcore.AccessToken{}, c.err
	}
	return azcore.AccessToken{Token: "azure-access-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func (c *fakeAzureCredential) requestedScopes() [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.scopes)
}

func requireAPIVersion(version string, h func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("api-version"); got != version {
			http.Error(w, "api-version "+got+", want "+version, http.StatusBadRequest)
			return
		}
		h(w, r)
	}
}

func TestNewAzureProviderSendsAManagementTokenAndReusesIt(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"displayName":"Dev","state":"Enabled"}`))
	}))
	defer srv.Close()
	cred := &fakeAzureCredential{}
	p := NewAzureProvider(context.Background(), cred, sub)
	if p.base != "https://management.azure.com" || p.Name() != ProviderAzure || !slices.Equal(p.ResourceTypes(), []string{AzureTypeVM, AzureTypePostgres, AzureTypeMySQL}) {
		t.Fatalf("provider = base %s, name %s, types %v", p.base, p.Name(), p.ResourceTypes())
	}
	p.base = srv.URL

	for range 2 {
		if err := p.ValidateConnectivity(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	want := "GET /subscriptions/" + sub + "?api-version=2022-12-01 Bearer azure-access-token"
	if len(seen) != 2 || seen[0] != want || seen[1] != want {
		t.Errorf("requests = %q, want two of %q", seen, want)
	}
	if scopes := cred.requestedScopes(); len(scopes) != 1 || !slices.Equal(scopes[0], []string{"https://management.azure.com/.default"}) {
		t.Errorf("token requests = %v, want one for the management scope, reused while it is valid", scopes)
	}

	failing := NewAzureProvider(context.Background(), &fakeAzureCredential{err: errors.New("workload identity token file not found")}, sub)
	failing.base = srv.URL
	if err := failing.ValidateConnectivity(context.Background()); err == nil || !strings.Contains(err.Error(), "workload identity token file not found") {
		t.Errorf("ValidateConnectivity without a token = %v, want the credential's error", err)
	}
	if len(seen) != 2 {
		t.Errorf("a request went out without a token: %q", seen)
	}
}

func TestAzureValidateConnectivity(t *testing.T) {
	longBody := strings.Repeat("x", 400)
	cases := []struct {
		name    string
		sub     string
		reply   func(http.ResponseWriter, *http.Request)
		wantErr string
	}{
		{"enabled", sub, jsonReply(map[string]string{"displayName": "Dev", "state": "Enabled"}), ""},
		{"no state reported", sub, jsonReply(map[string]string{"displayName": "Dev"}), ""},
		{"disabled", sub, jsonReply(map[string]string{"displayName": "Dev", "state": "Disabled"}), "subscription Dev is Disabled"},
		{"past due", sub, jsonReply(map[string]string{"displayName": "Dev", "state": "PastDue"}), "subscription Dev is PastDue"},
		{"no subscription configured", "", nil, "no Azure subscription ID is configured"},
		{"forbidden", sub, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"AuthorizationFailed","message":"The client does not have authorization"}}`))
		}, "could not read subscription " + sub + ": HTTP 403: The client does not have authorization"},
		{"gateway error page", sub, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(longBody))
		}, "could not read subscription " + sub + ": HTTP 502: " + longBody[:300] + "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeCloud{routes: map[string]func(http.ResponseWriter, *http.Request){}}
			if c.reply != nil {
				f.routes["GET /subscriptions/"+sub] = requireAPIVersion("2022-12-01", c.reply)
			}
			rest, base := testREST(t, f)
			err := (&AzureProvider{subscription: c.sub, rest: rest, base: base}).ValidateConnectivity(context.Background())
			if c.wantErr == "" && err != nil {
				t.Fatalf("ValidateConnectivity = %v, want success", err)
			}
			if c.wantErr != "" && (err == nil || err.Error() != c.wantErr) {
				t.Fatalf("ValidateConnectivity = %v, want %q", err, c.wantErr)
			}
			if c.reply == nil && len(f.requests) != 0 {
				t.Errorf("requests without a subscription: %v", f.requests)
			}
		})
	}
}

func TestNewAzureCredential(t *testing.T) {
	unsetEnvironment(t, "AZURE_TOKEN_CREDENTIALS", "AZURE_TENANT_ID", "AZURE_CLIENT_ID", "AZURE_CLIENT_SECRET",
		"AZURE_CLIENT_CERTIFICATE_PATH", "AZURE_USERNAME", "AZURE_PASSWORD", "AZURE_FEDERATED_TOKEN_FILE", "AZURE_AUTHORITY_HOST")

	if cred, err := NewAzureCredential("", "client", "secret"); err == nil || cred != nil || err.Error() != "a tenant ID is required with a client secret" {
		t.Errorf("client secret without a tenant = %v, %v", cred, err)
	}
	cred, err := NewAzureCredential("contoso.onmicrosoft.com", "client", "secret")
	if _, ok := cred.(*azidentity.ClientSecretCredential); err != nil || !ok {
		t.Errorf("client secret with a tenant = %T, %v; want a client secret credential", cred, err)
	}
	for _, c := range []struct{ tenant, clientID, secret string }{{"", "", ""}, {"contoso.onmicrosoft.com", "client", ""}, {"", "", "secret"}} {
		cred, err := NewAzureCredential(c.tenant, c.clientID, c.secret)
		if _, ok := cred.(*azidentity.DefaultAzureCredential); err != nil || !ok {
			t.Errorf("NewAzureCredential(%q, %q, %q) = %T, %v; want the default chain", c.tenant, c.clientID, c.secret, cred, err)
		}
	}
}

func TestAzureReadiness(t *testing.T) {
	vmID := "/subscriptions/" + sub + "/resourceGroups/dev/providers/Microsoft.Compute/virtualMachines/build-agent"
	pgID := "/subscriptions/" + sub + "/resourceGroups/dev/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg-dev"
	myID := "/subscriptions/" + sub + "/resourceGroups/dev/providers/Microsoft.DBforMySQL/flexibleServers/my-dev"
	vmView := func(codes ...string) func(http.ResponseWriter, *http.Request) {
		statuses := make([]map[string]string, 0, len(codes))
		for _, code := range codes {
			statuses = append(statuses, map[string]string{"code": code})
		}
		return requireAPIVersion("2024-07-01", jsonReply(map[string]any{"statuses": statuses}))
	}
	server := func(version, state string) func(http.ResponseWriter, *http.Request) {
		return requireAPIVersion(version, jsonReply(map[string]any{"properties": map[string]string{"state": state}}))
	}
	cases := []struct {
		name    string
		target  finopsv1.ExternalTarget
		active  bool
		route   string
		reply   func(http.ResponseWriter, *http.Request)
		want    bool
		wantErr string
	}{
		{"running VM is up", finopsv1.ExternalTarget{Type: AzureTypeVM, Identifier: vmID}, true, "GET " + vmID + "/instanceView", vmView("ProvisioningState/succeeded", "PowerState/running"), true, ""},
		{"starting VM is not up", finopsv1.ExternalTarget{Type: AzureTypeVM, Identifier: vmID}, true, "GET " + vmID + "/instanceView", vmView("PowerState/starting"), false, ""},
		{"powered-off VM is down", finopsv1.ExternalTarget{Type: AzureTypeVM, Identifier: vmID}, false, "GET " + vmID + "/instanceView", vmView("PowerState/stopped"), true, ""},
		{"deallocating VM is not down", finopsv1.ExternalTarget{Type: AzureTypeVM, Identifier: vmID}, false, "GET " + vmID + "/instanceView", vmView("PowerState/deallocating"), false, ""},
		{"VM without a power state is not up", finopsv1.ExternalTarget{Type: AzureTypeVM, Identifier: vmID}, true, "GET " + vmID + "/instanceView", vmView("ProvisioningState/updating"), false, ""},
		{"VM without a power state is not down", finopsv1.ExternalTarget{Type: AzureTypeVM, Identifier: vmID}, false, "GET " + vmID + "/instanceView", vmView("ProvisioningState/updating"), false, ""},
		{"starting PostgreSQL is not up", finopsv1.ExternalTarget{Type: AzureTypePostgres, Identifier: pgID}, true, "GET " + pgID, server("2024-08-01", "Starting"), false, ""},
		{"stopped MySQL is down", finopsv1.ExternalTarget{Type: AzureTypeMySQL, Identifier: myID}, false, "GET " + myID, server("2023-12-30", "Stopped"), true, ""},
		{"ready MySQL is not down", finopsv1.ExternalTarget{Type: AzureTypeMySQL, Identifier: myID}, false, "GET " + myID, server("2023-12-30", "Ready"), false, ""},
		{"VM API error", finopsv1.ExternalTarget{Type: AzureTypeVM, Identifier: vmID}, true, "GET " + vmID + "/instanceView", status(http.StatusNotFound), false, "HTTP 404"},
		{"server API error", finopsv1.ExternalTarget{Type: AzureTypeMySQL, Identifier: myID}, true, "GET " + myID, status(http.StatusForbidden), false, "HTTP 403"},
		{"a PostgreSQL ID used as a VM", finopsv1.ExternalTarget{Type: AzureTypeVM, Identifier: pgID}, true, "", nil, false, "is not the resource ID of an Azure vm"},
		{"unsupported type", finopsv1.ExternalTarget{Type: "aks", Identifier: vmID}, true, "", nil, false, "unsupported Azure resource type: aks"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeCloud{routes: map[string]func(http.ResponseWriter, *http.Request){}}
			if c.route != "" {
				f.routes[c.route] = c.reply
			}
			rest, base := testREST(t, f)
			ready, err := (&AzureProvider{subscription: sub, rest: rest, base: base}).IsReady(context.Background(), c.target, c.active)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) || ready {
					t.Fatalf("IsReady = %v, %v; want false and an error containing %q", ready, err, c.wantErr)
				}
			} else if err != nil || ready != c.want {
				t.Fatalf("IsReady = %v, %v; want %v", ready, err, c.want)
			}
			if c.route == "" && len(f.requests) != 0 {
				t.Errorf("a rejected target was requested: %v", f.requests)
			}
			if c.route != "" && (len(f.requests) != 1 || f.requests[0] != c.route) {
				t.Errorf("requests = %v, want only %s", f.requests, c.route)
			}
		})
	}
}

func TestAzureScaleCallsTheActionOfEachKind(t *testing.T) {
	vmID := "/subscriptions/" + sub + "/resourceGroups/dev/providers/Microsoft.Compute/virtualMachines/build-agent"
	pgID := "/subscriptions/" + sub + "/resourceGroups/dev/providers/Microsoft.DBforPostgreSQL/flexibleServers/pg-dev"
	myID := "/subscriptions/" + sub + "/resourceGroups/dev/providers/Microsoft.DBforMySQL/flexibleServers/my-dev"
	f := &fakeCloud{routes: map[string]func(http.ResponseWriter, *http.Request){
		"POST " + vmID + "/start":      requireAPIVersion("2024-07-01", status(http.StatusAccepted)),
		"POST " + vmID + "/deallocate": requireAPIVersion("2024-07-01", status(http.StatusAccepted)),
		"POST " + pgID + "/stop":       requireAPIVersion("2024-08-01", status(http.StatusAccepted)),
		"POST " + myID + "/start":      requireAPIVersion("2023-12-30", status(http.StatusOK)),
		"POST " + myID + "/stop": requireAPIVersion("2023-12-30", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"InternalServerError","message":"The server had an internal error"}}`))
		}),
	}}
	rest, base := testREST(t, f)
	p := &AzureProvider{subscription: sub, rest: rest, base: base}
	ctx := context.Background()

	for _, c := range []struct {
		target finopsv1.ExternalTarget
		active bool
	}{
		{finopsv1.ExternalTarget{Type: AzureTypeVM, Identifier: vmID}, true},
		{finopsv1.ExternalTarget{Type: AzureTypeVM, Identifier: vmID}, false},
		{finopsv1.ExternalTarget{Type: AzureTypePostgres, Identifier: pgID}, false},
		{finopsv1.ExternalTarget{Type: AzureTypeMySQL, Identifier: myID}, true},
	} {
		if err := p.Scale(ctx, c.target, c.active); err != nil {
			t.Errorf("Scale(%s, active=%v) = %v", c.target.Identifier, c.active, err)
		}
	}
	want := []string{"POST " + vmID + "/start", "POST " + vmID + "/deallocate", "POST " + pgID + "/stop", "POST " + myID + "/start"}
	if !slices.Equal(f.requests, want) {
		t.Errorf("requests = %v, want %v", f.requests, want)
	}

	err := p.Scale(ctx, finopsv1.ExternalTarget{Type: AzureTypeMySQL, Identifier: myID}, false)
	var api *apiError
	if !errors.As(err, &api) || api.Status != http.StatusInternalServerError || err.Error() != "HTTP 500: The server had an internal error" {
		t.Errorf("Scale on a failing server = %v, want the API error (only 409 counts as done)", err)
	}
	if err := p.Scale(ctx, finopsv1.ExternalTarget{Type: "aks", Identifier: vmID}, true); err == nil || err.Error() != "unsupported Azure resource type: aks" {
		t.Errorf("Scale(unsupported) = %v", err)
	}
}

func TestAzureDiscoverFollowsPagingAndReportsErrors(t *testing.T) {
	myID := "/subscriptions/" + sub + "/resourceGroups/dev/providers/Microsoft.DBforMySQL/flexibleServers/"
	var base string
	f := &fakeCloud{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /subscriptions/" + sub + "/providers/Microsoft.DBforMySQL/flexibleServers": requireAPIVersion("2023-12-30", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") == "" {
				jsonReply(map[string]any{"nextLink": base + r.URL.Path + "?api-version=2023-12-30&page=2", "value": []any{
					map[string]any{"id": myID + "my-dev", "name": "my-dev", "location": "northeurope", "tags": map[string]string{"env": "dev"}, "properties": map[string]string{"state": "Ready"}},
					map[string]any{"id": myID + "my-prod", "name": "my-prod", "location": "northeurope", "tags": map[string]string{"env": "prod"}, "properties": map[string]string{"state": "Ready"}},
				}})(w, r)
				return
			}
			jsonReply(map[string]any{"value": []any{
				map[string]any{"id": myID + "my-qa", "name": "my-qa", "location": "westeurope", "tags": map[string]string{"env": "dev"}, "properties": map[string]string{"state": "Stopped"}},
			}})(w, r)
		}),
		"GET /subscriptions/" + sub + "/providers/Microsoft.Compute/virtualMachines": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("statusOnly") != "true" {
				http.Error(w, "statusOnly missing", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"does not have authorization to perform action 'Microsoft.Compute/virtualMachines/read'"}}`))
		},
	}}
	rest, srvURL := testREST(t, f)
	base = srvURL
	p := &AzureProvider{subscription: sub, rest: rest, base: base}
	ctx := context.Background()

	got, err := p.Discover(ctx, AzureTypeMySQL, map[string]string{"env": "dev"})
	if err != nil {
		t.Fatal(err)
	}
	want := []finopsv1.ExternalTarget{
		{Provider: ProviderAzure, Type: AzureTypeMySQL, Identifier: myID + "my-dev", Region: "northeurope", Status: "ready", Name: "my-dev"},
		{Provider: ProviderAzure, Type: AzureTypeMySQL, Identifier: myID + "my-qa", Region: "westeurope", Status: "stopped", Name: "my-qa"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Discover(mysql, env=dev) = %+v, want %+v", got, want)
	}

	if got, err := p.Discover(ctx, AzureTypeVM, nil); err == nil || got != nil ||
		err.Error() != "list Azure vm: HTTP 403: does not have authorization to perform action 'Microsoft.Compute/virtualMachines/read'" {
		t.Errorf("Discover(vm) on a forbidden subscription = %v, %v", got, err)
	}
	if got, err := p.Discover(ctx, "aks", nil); err == nil || got != nil || err.Error() != `discovery unsupported for Azure type "aks"` {
		t.Errorf("Discover(aks) = %v, %v", got, err)
	}
}
