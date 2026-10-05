package scaling

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"

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
