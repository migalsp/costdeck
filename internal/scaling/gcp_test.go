package scaling

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

const placeholderPrivateKey = "-----BEGIN PRIVATE KEY-----\nnot-used-until-a-token-is-needed\n-----END PRIVATE KEY-----\n"

func serviceAccountKey(t *testing.T, project, tokenURI, privateKeyPEM string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": project, "private_key_id": "key-1", "private_key": privateKeyPEM,
		"client_email": "costdeck@" + project + ".iam.gserviceaccount.com", "client_id": "1234567890", "token_uri": tokenURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func generatedPrivateKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestGCPServiceAccountTokenReachesTheComputeAPI(t *testing.T) {
	var mu sync.Mutex
	var scopes, apiCalls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/token" {
			if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
				http.Error(w, "bad grant", http.StatusBadRequest)
				return
			}
			parts := strings.Split(r.PostForm.Get("assertion"), ".")
			var claims struct {
				Scope string `json:"scope"`
			}
			if len(parts) == 3 {
				if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
					_ = json.Unmarshal(payload, &claims)
				}
			}
			scopes = append(scopes, claims.Scope)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"gcp-access-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		apiCalls = append(apiCalls, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"name":"token-project"}`))
	}))
	defer srv.Close()
	ctx := context.Background()

	creds, err := GCPCredentials(ctx, serviceAccountKey(t, "token-project", srv.URL+"/token", generatedPrivateKeyPEM(t)))
	if err != nil {
		t.Fatal(err)
	}
	if creds.ProjectID != "token-project" {
		t.Errorf("project = %q, want the key's project", creds.ProjectID)
	}
	p := NewGCPProvider(ctx, creds.TokenSource, creds.ProjectID)
	if p.computeBase != "https://compute.googleapis.com/compute/v1" || p.sqlBase != "https://sqladmin.googleapis.com/v1" ||
		p.Name() != ProviderGCP || !slices.Equal(p.ResourceTypes(), []string{GCPTypeGCE, GCPTypeCloudSQL}) {
		t.Fatalf("provider = compute %s, sql %s, name %s, types %v", p.computeBase, p.sqlBase, p.Name(), p.ResourceTypes())
	}
	p.computeBase = srv.URL

	for range 2 {
		if err := p.ValidateConnectivity(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(scopes, []string{"https://www.googleapis.com/auth/cloud-platform"}) {
		t.Errorf("token requests asked for scopes %q, want one cloud-platform token, reused", scopes)
	}
	want := "GET /projects/token-project Bearer gcp-access-token"
	if len(apiCalls) != 2 || apiCalls[0] != want || apiCalls[1] != want {
		t.Errorf("API calls = %q, want two of %q", apiCalls, want)
	}
}

func TestGCPCredentialsAcceptOnlyServiceAccountKeys(t *testing.T) {
	ctx := context.Background()
	for name, key := range map[string]string{
		"user credentials":       `{"type":"authorized_user","client_id":"x","client_secret":"y","refresh_token":"z"}`,
		"external account":       `{"type":"external_account","audience":"//iam.googleapis.com/x","subject_token_type":"urn:ietf:params:oauth:token-type:jwt","token_url":"https://evil.example.com/token","credential_source":{"url":"http://169.254.169.254/latest"}}`,
		"impersonated account":   `{"type":"impersonated_service_account","service_account_impersonation_url":"https://evil.example.com"}`,
		"not JSON":               `credentials`,
		"service account in GDC": `{"type":"gdch_service_account","project":"p"}`,
	} {
		if creds, err := GCPCredentials(ctx, []byte(key)); err == nil || creds != nil {
			t.Errorf("%s: GCPCredentials = %v, %v; want a rejection", name, creds, err)
		}
	}
	creds, err := GCPCredentials(ctx, serviceAccountKey(t, "beta", "https://oauth2.googleapis.com/token", placeholderPrivateKey))
	if err != nil || creds.ProjectID != "beta" {
		t.Errorf("service account key = %v, %v; want credentials for project beta", creds, err)
	}
}

func TestGCPCredentialsWithoutAKeyUseTheDefaultChain(t *testing.T) {
	file := filepath.Join(t.TempDir(), "application_default_credentials.json")
	if err := os.WriteFile(file, serviceAccountKey(t, "from-environment", "https://oauth2.googleapis.com/token", placeholderPrivateKey), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", file)

	creds, err := GCPCredentials(context.Background(), nil)
	if err != nil || creds.ProjectID != "from-environment" {
		t.Errorf("GCPCredentials(nil) = %v, %v; want the credentials GOOGLE_APPLICATION_CREDENTIALS points at", creds, err)
	}
}

func TestGCPValidateConnectivity(t *testing.T) {
	f := &fakeCloud{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/demo": jsonReply(map[string]string{"name": "demo"}),
		"GET /projects/locked": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Required 'compute.projects.get' permission for 'projects/locked'"}}`))
		},
	}}
	rest, base := testREST(t, f)
	ctx := context.Background()

	if err := (&GCPProvider{project: "demo", rest: rest, computeBase: base}).ValidateConnectivity(ctx); err != nil {
		t.Errorf("readable project = %v", err)
	}
	err := (&GCPProvider{project: "locked", rest: rest, computeBase: base}).ValidateConnectivity(ctx)
	if err == nil || err.Error() != "could not read project locked: HTTP 403: Required 'compute.projects.get' permission for 'projects/locked'" {
		t.Errorf("forbidden project = %v", err)
	}
	requests := len(f.requests)
	if err := (&GCPProvider{rest: rest, computeBase: base}).ValidateConnectivity(ctx); err == nil || err.Error() != "no Google Cloud project ID is configured" {
		t.Errorf("no project = %v", err)
	}
	if len(f.requests) != requests {
		t.Errorf("a request went out without a project: %v", f.requests)
	}
}

func TestGCPReadiness(t *testing.T) {
	gce := func(instanceStatus string) func(http.ResponseWriter, *http.Request) {
		return jsonReply(map[string]string{"status": instanceStatus})
	}
	sql := func(policy, state string) func(http.ResponseWriter, *http.Request) {
		return jsonReply(map[string]any{"state": state, "settings": map[string]string{"activationPolicy": policy}})
	}
	vm := finopsv1.ExternalTarget{Type: GCPTypeGCE, Identifier: "europe-west1-b/dev-vm"}
	db := finopsv1.ExternalTarget{Type: GCPTypeCloudSQL, Identifier: "pg-dev"}
	const vmPath = "GET /projects/demo/zones/europe-west1-b/instances/dev-vm"
	const dbPath = "GET /projects/demo/instances/pg-dev"
	cases := []struct {
		name    string
		target  finopsv1.ExternalTarget
		active  bool
		route   string
		reply   func(http.ResponseWriter, *http.Request)
		want    bool
		wantErr string
	}{
		{"running instance is up", vm, true, vmPath, gce("RUNNING"), true, ""},
		{"staging instance is not up", vm, true, vmPath, gce("STAGING"), false, ""},
		{"stopped instance is down", vm, false, vmPath, gce("STOPPED"), true, ""},
		{"stopping instance is not down", vm, false, vmPath, gce("STOPPING"), false, ""},
		{"instance API error", vm, true, vmPath, status(http.StatusNotFound), false, "HTTP 404"},
		{"always-on runnable database is up", db, true, dbPath, sql("ALWAYS", "RUNNABLE"), true, ""},
		{"always-on database in maintenance is not up", db, true, dbPath, sql("ALWAYS", "MAINTENANCE"), false, ""},
		{"never-on database is not up even while runnable", db, true, dbPath, sql("NEVER", "RUNNABLE"), false, ""},
		{"never-on database is down", db, false, dbPath, sql("NEVER", "RUNNABLE"), true, ""},
		{"always-on database is not down", db, false, dbPath, sql("ALWAYS", "RUNNABLE"), false, ""},
		{"database API error", db, false, dbPath, status(http.StatusForbidden), false, "HTTP 403"},
		{"malformed instance ID", finopsv1.ExternalTarget{Type: GCPTypeGCE, Identifier: "dev-vm"}, true, "", nil, false, "is not a Compute Engine instance"},
		{"malformed database name", finopsv1.ExternalTarget{Type: GCPTypeCloudSQL, Identifier: "../pg"}, true, "", nil, false, "is not a Cloud SQL instance name"},
		{"unsupported type", finopsv1.ExternalTarget{Type: "gke", Identifier: "x"}, true, "", nil, false, "unsupported Google Cloud resource type: gke"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeCloud{routes: map[string]func(http.ResponseWriter, *http.Request){}}
			if c.route != "" {
				f.routes[c.route] = c.reply
			}
			rest, base := testREST(t, f)
			ready, err := (&GCPProvider{project: "demo", rest: rest, computeBase: base, sqlBase: base}).IsReady(context.Background(), c.target, c.active)
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
		})
	}
}

func TestGCPScaleStartsComputeAndSetsCloudSQLPolicy(t *testing.T) {
	sqlCalls := 0
	f := &fakeCloud{routes: map[string]func(http.ResponseWriter, *http.Request){
		"POST /projects/demo/zones/us-east1-c/instances/batch-vm/start": jsonReply(map[string]string{"name": "op-1"}),
		"PATCH /projects/demo/instances/pg-dev":                         jsonReply(map[string]string{"name": "op-2"}),
		"PATCH /projects/demo/instances/pg-busy": func(w http.ResponseWriter, _ *http.Request) {
			sqlCalls++
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"message":"Operation failed because another operation was already in progress."}}`))
		},
		"PATCH /projects/demo/instances/pg-broken": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid request: instance is suspended"}}`))
		},
		"POST /projects/demo/zones/us-east1-c/instances/busy-vm/stop": status(http.StatusConflict),
	}}
	rest, base := testREST(t, f)
	p := &GCPProvider{project: "demo", rest: rest, computeBase: base, sqlBase: base}
	ctx := context.Background()

	if err := p.Scale(ctx, finopsv1.ExternalTarget{Type: GCPTypeGCE, Identifier: "us-east1-c/batch-vm"}, true); err != nil {
		t.Fatalf("start instance: %v", err)
	}
	if err := p.Scale(ctx, finopsv1.ExternalTarget{Type: GCPTypeCloudSQL, Identifier: "pg-dev"}, false); err != nil {
		t.Fatalf("stop database: %v", err)
	}
	if !slices.Equal(f.requests, []string{"POST /projects/demo/zones/us-east1-c/instances/batch-vm/start", "PATCH /projects/demo/instances/pg-dev"}) {
		t.Errorf("requests = %v", f.requests)
	}
	if f.bodies[1] != `{"settings":{"activationPolicy":"NEVER"}}` {
		t.Errorf("Cloud SQL stop body = %s, want only the NEVER activation policy", f.bodies[1])
	}

	if err := p.Scale(ctx, finopsv1.ExternalTarget{Type: GCPTypeCloudSQL, Identifier: "pg-busy"}, true); err != nil || sqlCalls != 1 {
		t.Errorf("a database busy with another operation = %v after %d calls, want success for the next reconcile to retry", err, sqlCalls)
	}
	if err := p.Scale(ctx, finopsv1.ExternalTarget{Type: GCPTypeCloudSQL, Identifier: "pg-broken"}, true); err == nil || err.Error() != "HTTP 400: Invalid request: instance is suspended" {
		t.Errorf("a rejected policy change = %v", err)
	}
	if err := p.Scale(ctx, finopsv1.ExternalTarget{Type: GCPTypeGCE, Identifier: "us-east1-c/busy-vm"}, false); err == nil || !strings.Contains(err.Error(), "HTTP 409") {
		t.Errorf("a Compute Engine conflict = %v, want it returned", err)
	}

	requests := len(f.requests)
	for _, target := range []finopsv1.ExternalTarget{
		{Type: GCPTypeCloudSQL, Identifier: "PG_Upper"},
		{Type: GCPTypeCloudSQL, Identifier: "pg/../../other"},
		{Type: "gke", Identifier: "cluster"},
	} {
		if err := p.Scale(ctx, target, true); err == nil {
			t.Errorf("Scale(%+v) = nil, want a rejection", target)
		}
	}
	if len(f.requests) != requests {
		t.Errorf("rejected targets were requested: %v", f.requests[requests:])
	}
}

func TestGCPDiscoverCloudSQLPagesAndErrors(t *testing.T) {
	f := &fakeCloud{routes: map[string]func(http.ResponseWriter, *http.Request){
		"GET /projects/demo/instances": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("pageToken") == "" {
				jsonReply(map[string]any{"nextPageToken": "p2", "items": []any{
					map[string]any{"name": "pg-dev", "region": "europe-west1", "state": "RUNNABLE", "settings": map[string]any{"activationPolicy": "ALWAYS", "userLabels": map[string]string{"env": "dev"}}},
					map[string]any{"name": "pg-prod", "region": "europe-west1", "state": "RUNNABLE", "settings": map[string]any{"activationPolicy": "ALWAYS", "userLabels": map[string]string{"env": "prod"}}},
				}})(w, r)
				return
			}
			jsonReply(map[string]any{"items": []any{
				map[string]any{"name": "pg-qa", "region": "us-east1", "state": "RUNNABLE", "settings": map[string]any{"activationPolicy": "NEVER", "userLabels": map[string]string{"env": "dev"}}},
			}})(w, r)
		},
		"GET /projects/locked/instances": status(http.StatusForbidden),
		"GET /projects/locked/aggregated/instances": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"Required 'compute.instances.list' permission"}}`))
		},
	}}
	rest, base := testREST(t, f)
	ctx := context.Background()
	p := &GCPProvider{project: "demo", rest: rest, computeBase: base, sqlBase: base}

	got, err := p.Discover(ctx, GCPTypeCloudSQL, map[string]string{"env": "dev"})
	if err != nil {
		t.Fatal(err)
	}
	want := []finopsv1.ExternalTarget{
		{Provider: ProviderGCP, Type: GCPTypeCloudSQL, Identifier: "pg-dev", Region: "europe-west1", Status: "runnable", Name: "pg-dev"},
		{Provider: ProviderGCP, Type: GCPTypeCloudSQL, Identifier: "pg-qa", Region: "us-east1", Status: "stopped", Name: "pg-qa"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Discover(cloudsql, env=dev) = %+v, want %+v", got, want)
	}

	locked := &GCPProvider{project: "locked", rest: rest, computeBase: base, sqlBase: base}
	if got, err := locked.Discover(ctx, GCPTypeCloudSQL, nil); err == nil || got != nil || !strings.HasPrefix(err.Error(), "list Cloud SQL instances: HTTP 403") {
		t.Errorf("Discover(cloudsql) forbidden = %v, %v", got, err)
	}
	if got, err := locked.Discover(ctx, GCPTypeGCE, nil); err == nil || got != nil ||
		err.Error() != "list Compute Engine instances: HTTP 403: Required 'compute.instances.list' permission" {
		t.Errorf("Discover(gce) forbidden = %v, %v", got, err)
	}
	if got, err := p.Discover(ctx, "gke", nil); err == nil || got != nil || err.Error() != `discovery unsupported for Google Cloud type "gke"` {
		t.Errorf("Discover(gke) = %v, %v", got, err)
	}
}
