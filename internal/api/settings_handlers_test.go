package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
)

const fakeServiceAccountKey = `{\"type\":\"service_account\",\"client_email\":\"cost@proj.iam.gserviceaccount.com\",\"private_key\":\"gcp-private-key-value\"}`

var settingsSecrets = []string{
	"AKIA-SECRET-ID", "aws-secret-value", "azure-secret-value", "gcp-private-key-value", "ai-api-key-value",
	"webex-bot-token-value", "webex-webhook-secret-value", "vm-bearer-value", "vm-user-value", "vm-password-value",
	"entra-secret-value", "oidc-secret-value",
}

const everyCredential = `{
	"providers": {
		"aws": {"enabled": true, "region": "eu-west-1", "accessKeyId": "AKIA-SECRET-ID", "secretAccessKey": "aws-secret-value", "discoveryTags": {"env": "dev"}},
		"azure": {"enabled": true, "subscriptionId": "00000000-0000-0000-0000-000000000001", "tenantId": "tenant-1", "clientId": "azure-client", "clientSecret": "azure-secret-value"},
		"gcp": {"enabled": true, "projectId": "proj-1", "serviceAccountJson": "` + fakeServiceAccountKey + `"}
	},
	"integrations": {
		"ai": {"enabled": true, "provider": "openai", "model": "gpt-4o", "apiKey": "ai-api-key-value"},
		"messenger": {"webex": {"enabled": true, "roomId": " room-1 ", "botToken": "webex-bot-token-value", "webhookSecret": "webex-webhook-secret-value"}},
		"victoriaMetrics": {"enabled": true, "endpoint": "http://vmselect.monitoring:8481/select/0/prometheus", "retentionDays": 14,
			"bearerToken": "vm-bearer-value", "username": "vm-user-value", "password": "vm-password-value"}
	},
	"auth": {
		"entra": {"tenantId": "contoso", "clientId": "entra-app", "clientSecret": "entra-secret-value"},
		"oidc": {"issuerUrl": "https://idp.example.com", "clientId": "costdeck", "clientSecret": "oidc-secret-value"}
	}
}`

func secretData(t *testing.T, server *Server, name string) map[string]string {
	t.Helper()
	var s corev1.Secret
	if err := server.Client.Get(context.Background(), client.ObjectKey{Namespace: operatorNamespace, Name: name}, &s); err != nil {
		t.Fatalf("Secret %s: %v", name, err)
	}
	out := map[string]string{}
	for k, v := range s.Data {
		out[k] = string(v)
	}
	return out
}

func storedSpec(t *testing.T, server *Server) finopsv1.CostDeckConfigSpec {
	t.Helper()
	cfg, err := config.Get(context.Background(), server.Client)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Spec
}

func assertNoSecretIn(t *testing.T, where, body string) {
	t.Helper()
	for _, s := range settingsSecrets {
		if strings.Contains(body, s) {
			t.Errorf("%s returned the secret %q", where, s)
		}
	}
}

func TestSettingsStoreCredentialsInSecretsAndReturnOnlyFlags(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()

	rr := send(t, server, http.MethodPut, "/api/settings", everyCredential)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rr.Code, rr.Body.String())
	}
	assertNoSecretIn(t, "PUT /api/settings", rr.Body.String())
	got := send(t, server, http.MethodGet, "/api/settings", "")
	assertNoSecretIn(t, "GET /api/settings", got.Body.String())

	s := decodeInto[SettingsResponse](t, got)
	p, i, a := s.Providers, s.Integrations, s.Auth
	flags := map[string]bool{
		"aws":   p.AWS != nil && p.AWS.HasCredentials && p.AWS.Region == "eu-west-1" && p.AWS.DiscoveryTags["env"] == "dev",
		"azure": p.Azure != nil && p.Azure.HasCredentials && p.Azure.TenantID == "tenant-1",
		"gcp":   p.GCP != nil && p.GCP.HasCredentials && p.GCP.ProjectID == "proj-1",
		"ai":    i.AI != nil && i.AI.HasCredentials && i.AI.Model == "gpt-4o",
		"webex": i.Messenger != nil && i.Messenger.Webex != nil && i.Messenger.Webex.HasCredentials && i.Messenger.Webex.RoomID == "room-1",
		"vm":    i.VictoriaMetrics != nil && i.VictoriaMetrics.HasCredentials && i.VictoriaMetrics.RetentionDays == 14,
		"entra": a.Entra != nil && a.Entra.HasClientSecret,
		"oidc":  a.OIDC != nil && a.OIDC.HasClientSecret,
	}
	for section, ok := range flags {
		if !ok {
			t.Errorf("%s settings do not report their stored credentials: %s", section, got.Body.String())
		}
	}

	for name, want := range map[string]map[string]string{
		"costdeck-aws-credentials":   {"AWS_ACCESS_KEY_ID": "AKIA-SECRET-ID", "AWS_SECRET_ACCESS_KEY": "aws-secret-value", "AWS_REGION": "eu-west-1"},
		"costdeck-azure-credentials": {"AZURE_CLIENT_ID": "azure-client", "AZURE_CLIENT_SECRET": "azure-secret-value", "AZURE_TENANT_ID": "tenant-1"},
		"costdeck-ai-credentials":    {"API_KEY": "ai-api-key-value"},
		"costdeck-webex-credentials": {"BOT_TOKEN": "webex-bot-token-value", "WEBHOOK_SECRET": "webex-webhook-secret-value"},
		"costdeck-vm-credentials":    {"BEARER_TOKEN": "vm-bearer-value", "USERNAME": "vm-user-value", "PASSWORD": "vm-password-value"},
		"costdeck-entra-credentials": {"CLIENT_SECRET": "entra-secret-value"},
		"costdeck-oidc-credentials":  {"CLIENT_SECRET": "oidc-secret-value"},
	} {
		if got := secretData(t, server, name); !reflect.DeepEqual(got, want) {
			t.Errorf("Secret %s = %v, want %v", name, got, want)
		}
	}
	if key := secretData(t, server, "costdeck-gcp-credentials")["credentials.json"]; !strings.Contains(key, "gcp-private-key-value") {
		t.Errorf("GCP key = %q", key)
	}
}

func TestSavingOneSettingsSectionLeavesTheOthersAlone(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	if rr := send(t, server, http.MethodPut, "/api/settings", everyCredential); rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rr.Code, rr.Body.String())
	}
	before := storedSpec(t, server)
	secretsBefore := map[string]map[string]string{}
	for _, name := range []string{"costdeck-aws-credentials", "costdeck-ai-credentials", "costdeck-webex-credentials", "costdeck-vm-credentials"} {
		secretsBefore[name] = secretData(t, server, name)
	}

	for _, body := range []string{
		`{"pricing":{"currency":"chf","cpuCoreHour":"0.05","memoryGiBHour":"0.006"}}`,
		`{"integrations":{"mcp":{"enabled":true}}}`,
		`{"features":{"cloudPricingApi":true}}`,
		`{"dashboardUrl":"https://costdeck.example.com/"}`,
	} {
		if rr := send(t, server, http.MethodPut, "/api/settings", body); rr.Code != http.StatusOK {
			t.Fatalf("PUT %s = %d %s", body, rr.Code, rr.Body.String())
		}
	}
	after := storedSpec(t, server)
	if after.Pricing.Currency != "CHF" || after.Pricing.CPUCoreHour != "0.05" || after.Integrations.MCP == nil || !after.Integrations.MCP.Enabled ||
		!after.Features.CloudPricingAPI || after.DashboardURL != "https://costdeck.example.com" {
		t.Errorf("the saved sections did not apply: pricing %+v, mcp %+v, features %+v, url %q", after.Pricing, after.Integrations.MCP, after.Features, after.DashboardURL)
	}
	for section, pair := range map[string][2]any{
		"providers":       {before.Providers, after.Providers},
		"ai":              {before.Integrations.AI, after.Integrations.AI},
		"messenger":       {before.Integrations.Messenger, after.Integrations.Messenger},
		"victoriaMetrics": {before.Integrations.VictoriaMetrics, after.Integrations.VictoriaMetrics},
		"auth":            {before.Auth, after.Auth},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("saving other sections changed %s:\nbefore %+v\nafter  %+v", section, pair[0], pair[1])
		}
	}
	for name, want := range secretsBefore {
		if got := secretData(t, server, name); !reflect.DeepEqual(got, want) {
			t.Errorf("Secret %s changed to %v", name, got)
		}
	}
}

func TestWebexSettingsUpdateOneCredentialAndKeepTheOther(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	if rr := send(t, server, http.MethodPut, "/api/settings", everyCredential); rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rr.Code, rr.Body.String())
	}
	if rr := send(t, server, http.MethodPut, "/api/settings", `{"integrations":{"messenger":{"webex":{"webhookSecret":""}}}}`); rr.Code != http.StatusOK {
		t.Fatalf("clearing the webhook secret = %d %s", rr.Code, rr.Body.String())
	}
	if got := secretData(t, server, "costdeck-webex-credentials"); !reflect.DeepEqual(got, map[string]string{"BOT_TOKEN": "webex-bot-token-value"}) {
		t.Errorf("Webex Secret = %v, want the bot token kept and the webhook secret gone", got)
	}
	if rr := send(t, server, http.MethodPut, "/api/settings", `{"integrations":{"messenger":{"webex":{"botToken":"rotated-token"}}}}`); rr.Code != http.StatusOK {
		t.Fatalf("rotating the bot token = %d %s", rr.Code, rr.Body.String())
	}
	if got := secretData(t, server, "costdeck-webex-credentials"); !reflect.DeepEqual(got, map[string]string{"BOT_TOKEN": "rotated-token"}) {
		t.Errorf("Webex Secret = %v", got)
	}
}

func TestSettingsValidationRejectsTheWholeSave(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	if rr := send(t, server, http.MethodPut, "/api/settings", `{"pricing":{"currency":"usd"},"integrations":{"messenger":{"webex":{"enabled":true,"roomId":"room-1"}}}}`); rr.Code != http.StatusOK {
		t.Fatalf("seed = %d %s", rr.Code, rr.Body.String())
	}
	before := storedSpec(t, server)

	for body, want := range map[string]string{
		`{"providers":{"azure":{"subscriptionId":"not-a-guid"}}}`:                                                                                    "the Azure subscription ID must be a GUID",
		`{"providers":{"azure":{"clientId":"c","clientSecret":"s"}}}`:                                                                                "a tenant ID is required with a client secret",
		`{"providers":{"gcp":{"serviceAccountJson":"{\"type\":\"external_account\"}"}}}`:                                                             "paste a service account key",
		`{"providers":{"gcp":{"serviceAccountJson":"not json"}}}`:                                                                                    "the service account key is not valid JSON",
		`{"integrations":{"messenger":{"webex":{"roomId":"","notifyTransitions":true}}}}`:                                                            "transition notifications need a Webex space ID",
		`{"integrations":{"victoriaMetrics":{"endpoint":"ftp://vm:8428"}}}`:                                                                          "invalid endpoint",
		`{"integrations":{"victoriaMetrics":{"endpoint":"http://169.254.169.254:8428"}}}`:                                                            "link-local and cloud metadata addresses are not allowed",
		`{"pricing":{"cpuCoreHour":"cheap"}}`:                                                                                                        "rates must be plain decimal numbers",
		`{"pricing":{"currency":"EURO"}}`:                                                                                                            "currency must be a three-letter code",
		`{"pricing":{"llmModels":[{"model":"gpt-4o","inputPer1M":"2.5","outputPer1M":"10"},{"model":"gpt-4o","inputPer1M":"1","outputPer1M":"1"}]}}`: "priced twice",
		`{"pricing":{"llmModels":[{"model":"gpt-4o","inputPer1M":"$2","outputPer1M":"10"}]}}`:                                                        "plain decimal numbers such as 2.50",
		`{"reports":{"digest":{"frequency":"daily"}}}`:                                                                                               "frequency must be weekly or monthly",
		`{"reports":{"digest":{"time":"25:00"}}}`:                                                                                                    "time must be HH:MM",
		`{"reports":{"digest":{"timezone":"Mars/Olympus_Mons"}}}`:                                                                                    "unknown time zone",
		`{"billing":{"aws":{"tagValue":""}}}`:                                                                                                        "AWS billing needs the tag value",
		`{"billing":{"azure":{"resourceGroup":"bad group/name"}}}`:                                                                                   "Azure billing needs the node resource group",
		`{"billing":{"gcp":{"table":"proj.dataset","clusterName":"c"}}}`:                                                                             "Google Cloud billing needs the export table",
		`{"billing":{"aws":{"tagValue":"c"},"azure":{"resourceGroup":"MC_rg"}}}`:                                                                     "reconcile with one cloud",
		`{"billing":{"enabled":true}}`:                                                                                                               "say which cloud's bill to read",
		`{"dashboardUrl":"costdeck.example.com"}`:                                                                                                    "the dashboard URL must be an http(s) address",
		`{"pricing":`: "Invalid request body",
	} {
		rr := send(t, server, http.MethodPut, "/api/settings", body)
		if rr.Code != http.StatusBadRequest || !strings.Contains(errorMessage(t, rr), want) {
			t.Errorf("%s = %d %s, want 400 %q", body, rr.Code, rr.Body.String(), want)
		}
	}
	if after := storedSpec(t, server); !reflect.DeepEqual(before, after) {
		t.Errorf("a rejected save changed the stored settings:\nbefore %+v\nafter  %+v", before, after)
	}
	var secrets corev1.SecretList
	if err := server.Client.List(context.Background(), &secrets); err != nil || len(secrets.Items) != 0 {
		t.Errorf("rejected saves stored credentials: %d Secrets (%v)", len(secrets.Items), err)
	}
}

func TestRejectedSettingsSaveKeepsTheStoredCredentials(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	if rr := send(t, server, http.MethodPut, "/api/settings", `{"providers":{"aws":{"accessKeyId":"OLD-ID","secretAccessKey":"old-secret"}}}`); rr.Code != http.StatusOK {
		t.Fatalf("seed = %d %s", rr.Code, rr.Body.String())
	}
	rr := send(t, server, http.MethodPut, "/api/settings", `{"providers":{"aws":{"accessKeyId":"NEW-ID","secretAccessKey":"new-secret"}},"pricing":{"currency":"EURO"}}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT = %d, want 400 for the currency", rr.Code)
	}
	if got := secretData(t, server, "costdeck-aws-credentials"); got["AWS_ACCESS_KEY_ID"] != "OLD-ID" || got["AWS_SECRET_ACCESS_KEY"] != "old-secret" {
		t.Errorf("the dashboard reported the save as failed, yet the AWS Secret now holds %v", got)
	}
}

func TestProviderStatus(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	cfg := &finopsv1.CostDeckConfig{Name: "default", Namespace: operatorNamespace}
	cfg.Status.VictoriaMetrics = &finopsv1.ProviderStatus{Connected: true, Message: "42 series"}
	createAll(t, server.Client, cfg)

	if got := decodeInto[finopsv1.ProviderStatus](t, send(t, server, http.MethodGet, "/api/settings/providers/victoriametrics/status", "")); !got.Connected || got.Message != "42 series" {
		t.Errorf("VictoriaMetrics status = %+v", got)
	}
	for _, p := range []string{"aws", "azure", "gcp", "webex"} {
		rr := send(t, server, http.MethodGet, "/api/settings/providers/"+p+"/status", "")
		if got := decodeInto[finopsv1.ProviderStatus](t, rr); rr.Code != http.StatusOK || got.Connected || got.Error != "Provider not configured" {
			t.Errorf("%s status = %d %+v", p, rr.Code, got)
		}
	}
	if rr := send(t, server, http.MethodGet, "/api/settings/providers/oracle/status", ""); rr.Code != http.StatusBadRequest || errorMessage(t, rr) != "Unknown provider" {
		t.Errorf("unknown provider status = %d %s", rr.Code, rr.Body.String())
	}
	if rr := send(t, server, http.MethodPost, "/api/settings/providers/oracle/test", "{}"); rr.Code != http.StatusBadRequest || errorMessage(t, rr) != "Unknown provider: oracle" {
		t.Errorf("unknown provider test = %d %s", rr.Code, rr.Body.String())
	}
}

type connectionResult struct {
	Connected bool   `json:"connected"`
	Message   string `json:"message"`
	Error     string `json:"error"`
	Endpoint  string `json:"endpoint"`
}

func testProvider(t *testing.T, server *Server, provider, body string) connectionResult {
	t.Helper()
	rr := send(t, server, http.MethodPost, "/api/settings/providers/"+provider+"/test", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("test %s %s = %d %s", provider, body, rr.Code, rr.Body.String())
	}
	return decodeInto[connectionResult](t, rr)
}

func TestValidServiceAccountKeyAcceptsOnlyServiceAccounts(t *testing.T) {
	for raw, wantErr := range map[string]string{
		`{"type":"service_account","client_email":"a@b","private_key":"k"}`:                      "",
		`{"type":"external_account","credential_source":{"url":"http://169.254.169.254/token"}}`: "paste a service account key",
		`{"type":"service_account","private_key":"k"}`:                                           "paste a service account key",
		`{"type":"service_account","client_email":"a@b"}`:                                        "paste a service account key",
		`service_account`: "not valid JSON",
	} {
		err := validServiceAccountKey(raw)
		if (wantErr == "" && err != nil) || (wantErr != "" && (err == nil || !strings.Contains(err.Error(), wantErr))) {
			t.Errorf("validServiceAccountKey(%s) = %v, want %q", raw, err, wantErr)
		}
	}
}

func TestCloudProviderTestReportsWhatBlocksTheConnection(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	for _, tc := range []struct{ provider, body, want string }{
		{"gcp", `{"serviceAccountJson":"{\"type\":\"authorized_user\"}"}`, "paste a service account key"},
		{"gcp", `{"serviceAccountJson":"` + fakeServiceAccountKey + `"}`, "no Google Cloud project ID is configured"},
		{"azure", `{"clientId":"app","clientSecret":"s"}`, "the Azure credentials are not usable: a tenant ID is required with a client secret"},
	} {
		if got := testProvider(t, server, tc.provider, tc.body); got.Connected || !strings.Contains(got.Error, tc.want) {
			t.Errorf("%s %s = %+v, want %q", tc.provider, tc.body, got, tc.want)
		}
	}
	if _, err := config.SecretData(context.Background(), server.Client, "costdeck-gcp-credentials"); err == nil {
		t.Error("testing unsaved credentials must not store them")
	}
}

func fakeVictoriaMetricsValidation(t *testing.T, series string, tokens ...string) (*httptest.Server, *requestLog) {
	t.Helper()
	return recordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		allowed := false
		for _, token := range tokens {
			allowed = allowed || r.Header.Get("Authorization") == "Bearer "+token
		}
		if !allowed {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		result := []any{map[string]any{"metric": map[string]string{}, "value": []any{1730000000, series}}}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
	})
}

func TestVictoriaMetricsTestUsesTheFormThenTheStoredSettings(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	vm, calls := fakeVictoriaMetricsValidation(t, "42", "typed-token", "stored-token")
	createAll(t, server.Client, &corev1.Secret{Name: "vm-creds", Namespace: operatorNamespace, Data: map[string][]byte{"BEARER_TOKEN": []byte("stored-token")}})
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
		VictoriaMetrics: &finopsv1.VictoriaMetricsConfig{Enabled: true, Endpoint: vm.URL, SecretRef: "vm-creds"},
	}})

	got := testProvider(t, server, "victoriametrics", `{"endpoint":"`+vm.URL+`/select/0/prometheus/api/v1/query","bearerToken":"typed-token"}`)
	if !got.Connected || got.Endpoint != vm.URL+"/select/0/prometheus" {
		t.Errorf("typed settings = %+v, want the normalized endpoint", got)
	}
	if got := testProvider(t, server, "victoriametrics", `{}`); !got.Connected || got.Endpoint != vm.URL {
		t.Errorf("stored settings = %+v", got)
	}
	c := calls.all()
	if len(c) != 2 || c[0].Path != "/select/0/prometheus/api/v1/query" || c[0].Authorization != "Bearer typed-token" || c[1].Authorization != "Bearer stored-token" {
		t.Errorf("VictoriaMetrics calls = %+v", c)
	}
	if got := testProvider(t, server, "victoriametrics", `{"bearerToken":"wrong"}`); got.Connected || !strings.Contains(got.Error, "401") {
		t.Errorf("a rejected token = %+v", got)
	}
}

func TestVictoriaMetricsTestExplainsAnEmptyOrForbiddenEndpoint(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	empty, _ := fakeVictoriaMetricsValidation(t, "0", "t")
	if got := testProvider(t, server, "victoriametrics", `{"endpoint":"`+empty.URL+`","bearerToken":"t","labelSelector":"cluster=\"eu\""}`); got.Connected ||
		!strings.Contains(got.Error, `does the label selector {cluster="eu"} match your series?`) {
		t.Errorf("no series = %+v", got)
	}
	if got := testProvider(t, server, "victoriametrics", `{"endpoint":"http://169.254.169.254:8428"}`); got.Connected || !strings.Contains(got.Error, "link-local and cloud metadata addresses are not allowed") {
		t.Errorf("a metadata address = %+v, want it refused before any request", got)
	}
	if rr := send(t, server, http.MethodPost, "/api/settings/providers/victoriametrics/test", `{"endpoint":`); rr.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d", rr.Code)
	}
}

func fakeIssuer(t *testing.T, tenantPath string, acceptedSecret string) (*httptest.Server, *requestLog) {
	t.Helper()
	log := &requestLog{}
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issuer := srv.URL + tenantPath
		switch r.URL.Path {
		case tenantPath + "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case tenantPath + "/token":
			_ = r.ParseForm()
			secret := r.PostForm.Get("client_secret")
			if _, basic, ok := r.BasicAuth(); ok {
				secret = basic
			}
			log.add(&http.Request{URL: r.URL, Header: http.Header{"Authorization": {secret}}, Body: http.NoBody})
			if secret != acceptedSecret {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"t","token_type":"Bearer","expires_in":3600}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

func TestOIDCTestDiscoversTheIssuer(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	issuer, _ := fakeIssuer(t, "/realms/costdeck", "")
	issuerURL := issuer.URL + "/realms/costdeck"
	server := buildMockServerWithK8s()

	got := testProvider(t, server, "oidc", `{"issuerUrl":"`+issuerURL+`/","clientId":"costdeck","skipSslVerify":true}`)
	if !got.Connected || !strings.Contains(got.Message, "its sign-in endpoint is "+issuerURL+"/authorize") {
		t.Errorf("typed settings = %+v", got)
	}
	if got := testProvider(t, server, "oidc", `{"issuerUrl":"`+issuerURL+`","clientId":"costdeck"}`); got.Connected || !strings.Contains(got.Error, "discover the OpenID Connect issuer") {
		t.Errorf("a self-signed issuer without skipSslVerify = %+v, want TLS verified by default", got)
	}
	if got := testProvider(t, server, "oidc", `{"issuerUrl":"`+issuerURL+`","skipSslVerify":true}`); got.Connected || got.Error != "issuer URL and client ID are required" {
		t.Errorf("no client ID = %+v", got)
	}
	if rr := send(t, server, http.MethodPost, "/api/settings/providers/oidc/test", `{"issuerUrl":`); rr.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d", rr.Code)
	}

	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Auth: finopsv1.AuthConfig{OIDC: &finopsv1.OIDCConfig{IssuerURL: issuerURL, ClientID: "costdeck", SkipSSLVerify: true}}})
	if got := testProvider(t, server, "oidc", `{}`); !got.Connected {
		t.Errorf("stored settings = %+v", got)
	}
}

func TestEntraTestChecksTheClientSecret(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	authority, tokens := fakeIssuer(t, "/contoso/v2.0", "stored-entra-secret")
	server := buildMockServerWithK8s()
	createAll(t, server.Client, &corev1.Secret{Name: "costdeck-entra-credentials", Namespace: operatorNamespace, Data: map[string][]byte{"CLIENT_SECRET": []byte("stored-entra-secret")}})
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Auth: finopsv1.AuthConfig{Entra: &finopsv1.EntraConfig{TenantID: "contoso", ClientID: "app", ClientSecretRef: "costdeck-entra-credentials"}}})
	form := `"authorityHost":"` + authority.URL + `","skipSslVerify":true`

	if got := testProvider(t, server, "entra", `{`+form+`}`); !got.Connected || got.Message != "Tenant and client credentials are valid." {
		t.Errorf("stored secret = %+v", got)
	}
	if got := testProvider(t, server, "entra", `{`+form+`,"clientSecret":"typed-wrong-secret"}`); got.Connected || !strings.Contains(got.Error, "the client ID or secret was rejected") {
		t.Errorf("a wrong typed secret = %+v", got)
	}
	seen := map[string]bool{}
	for _, c := range tokens.all() {
		seen[c.Authorization] = true
	}
	if !seen["stored-entra-secret"] || !seen["typed-wrong-secret"] {
		t.Errorf("token endpoint saw %v, want the stored secret first and then the typed one", seen)
	}
	if got := testProvider(t, server, "entra", `{"tenantId":"","clientId":"",`+form+`}`); !got.Connected {
		t.Errorf("empty form fields must fall back to the stored ones: %+v", got)
	}
	if rr := send(t, server, http.MethodPost, "/api/settings/providers/entra/test", `{"tenantId":`); rr.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d", rr.Code)
	}
}

func TestEntraTestNeedsATenantAndClient(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	if got := testProvider(t, server, "entra", `{"clientId":"app"}`); got.Connected || got.Error != "tenant ID and client ID are required" {
		t.Errorf("no tenant = %+v", got)
	}
}

func fakeWebex(t *testing.T) *requestLog {
	t.Helper()
	log := &requestLog{}
	answerHostInProcess(t, "webexapis.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		switch r.URL.Path {
		case webexMePath:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "bot-id", "displayName": "CostDeck Bot"})
		case "/v1/rooms/room-ops":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "room-ops", "title": "Ops"})
		default:
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		}
	}))
	return log
}

func TestWebexTestChecksTheBotAndItsSpace(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	webex := fakeWebex(t)
	server := buildMockServerWithK8s()

	if got := testProvider(t, server, "webex", `{}`); got.Connected || got.Error != "the Webex credentials Secret has no BOT_TOKEN" {
		t.Errorf("no token anywhere = %+v", got)
	}
	if got := testProvider(t, server, "webex", `{"botToken":"typed-token","roomId":" room-ops "}`); !got.Connected || got.Message != `Connected as CostDeck Bot · polling space "Ops"` {
		t.Errorf("typed token and space = %+v", got)
	}
	if got := testProvider(t, server, "webex", `{"botToken":"typed-token","roomId":"room-gone"}`); got.Connected || !strings.Contains(got.Error, `bot "CostDeck Bot" cannot access space room-gone`) {
		t.Errorf("a space the bot is not in = %+v", got)
	}

	createAll(t, server.Client, &corev1.Secret{Name: "wx", Namespace: operatorNamespace, Data: map[string][]byte{"BOT_TOKEN": []byte("stored-token"), "WEBHOOK_SECRET": []byte("s")}})
	seedConfig(t, server, finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{Messenger: &finopsv1.MessengerIntegrationConfig{
		Webex: &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx", RoomID: "room-ops"},
	}}})
	if got := testProvider(t, server, "webex", `{}`); !got.Connected || got.Message != "Connected as CostDeck Bot · signed webhooks" {
		t.Errorf("stored settings = %+v", got)
	}
	calls := webex.all()
	if len(calls) < 2 || calls[0].Authorization != "Bearer typed-token" || calls[len(calls)-1].Authorization != "Bearer stored-token" {
		t.Errorf("Webex calls = %+v, want the typed token first and the stored one last", calls)
	}
	if rr := send(t, server, http.MethodPost, "/api/settings/providers/webex/test", `{"botToken":`); rr.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d", rr.Code)
	}
}
