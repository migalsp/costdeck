package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// oidcService signs in through the fake provider as a generic OpenID Connect issuer.
func oidcService(t *testing.T, f *fakeEntra, cfg finopsv1.OIDCConfig) *Service {
	cfg.Enabled, cfg.IssuerURL, cfg.ClientID, cfg.ClientSecretRef = true, f.issuer(), f.client, "oidc"
	return newTestService(t,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "oidc", Namespace: "costdeck"}, Data: map[string][]byte{SecretKeyClientSecret: []byte(f.secret)}},
		&finopsv1.CostDeckConfig{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
			Spec: finopsv1.CostDeckConfigSpec{Auth: finopsv1.AuthConfig{OIDC: &cfg}}})
}

func oidcSignIn(t *testing.T, svc *Service, f *fakeEntra, tamperState bool) (*httptest.ResponseRecorder, string) {
	t.Helper()
	rr := httptest.NewRecorder()
	svc.OIDC.HandleLogin(rr, httptest.NewRequest(http.MethodGet, "https://costdeck.example.com/api/auth/oidc/login?return=/budgets", nil))
	if rr.Code != http.StatusFound {
		t.Fatalf("login = %d: %s", rr.Code, rr.Body.String())
	}
	location := rr.Header().Get("Location")
	code, state := f.authorize(location)
	if tamperState {
		state = "forged"
	}
	cb := httptest.NewRequest(http.MethodGet, OIDCCallbackPath+"?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), nil)
	for _, c := range rr.Result().Cookies() {
		cb.AddCookie(c)
	}
	out := httptest.NewRecorder()
	svc.OIDC.HandleCallback(out, cb)
	return out, location
}

// A Keycloak-shaped token: groups as full paths, realm roles nested in realm_access.
func TestOIDCSignInWithKeycloakClaims(t *testing.T) {
	f := newFakeEntra(t)
	f.claims = map[string]any{"email": "ada@example.com", "groups": []string{"/costdeck-operators", "/everyone"},
		"realm_access": map[string]any{"roles": []string{"offline_access", "default-roles-costdeck"}}}
	svc := oidcService(t, f, finopsv1.OIDCConfig{RolesClaim: "realm_access.roles", Scopes: []string{"profile", "email", "groups"},
		GroupRoleMapping: map[string]string{"costdeck-operators": "operator"}})

	rr, location := oidcSignIn(t, svc, f, false)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/budgets" {
		t.Fatalf("callback = %d -> %q", rr.Code, rr.Header().Get("Location"))
	}
	u, _ := url.Parse(location)
	if u.Query().Get("scope") != "openid profile email groups" || !strings.Contains(location, url.QueryEscape("https://costdeck.example.com"+OIDCCallbackPath)) {
		t.Errorf("authorize request = %s", location)
	}
	id := sessionFrom(t, svc, rr)
	if id == nil || id.Role != RoleOperator || id.Subject != "oidc:s1" || id.Provider != "oidc" || id.Email != "ada@example.com" || id.Name != "Ada Lovelace" {
		t.Fatalf("session = %+v", id)
	}

	// A realm role named admin wins over the group's operator.
	f.claims["realm_access"] = map[string]any{"roles": []string{"admin"}}
	rr, _ = oidcSignIn(t, svc, f, false)
	if id := sessionFrom(t, svc, rr); id == nil || id.Role != RoleAdmin {
		t.Errorf("realm role admin: %+v", id)
	}
}

func TestOIDCRejectsForgedStateWrongNonceAndUnprovisionedUsers(t *testing.T) {
	f := newFakeEntra(t)
	svc := oidcService(t, f, finopsv1.OIDCConfig{})
	if rr, _ := oidcSignIn(t, svc, f, true); !strings.Contains(rr.Header().Get("Location"), "sso_error=state_mismatch") || sessionFrom(t, svc, rr) != nil {
		t.Errorf("forged state: %q", rr.Header().Get("Location"))
	}
	f.claims = map[string]any{"nonce": "replayed"}
	if rr, _ := oidcSignIn(t, svc, f, false); !strings.Contains(rr.Header().Get("Location"), "sso_error=invalid_token") || sessionFrom(t, svc, rr) != nil {
		t.Errorf("foreign nonce: %q", rr.Header().Get("Location"))
	}
	f.claims = nil
	no := false
	svc = oidcService(t, f, finopsv1.OIDCConfig{AutoProvision: &no, GroupRoleMapping: map[string]string{"admins": "admin"}})
	if rr, _ := oidcSignIn(t, svc, f, false); !strings.Contains(rr.Header().Get("Location"), "sso_error=user_not_provisioned") || sessionFrom(t, svc, rr) != nil {
		t.Errorf("unprovisioned user: %q", rr.Header().Get("Location"))
	}
	// Auto-provisioned users get the default role.
	svc = oidcService(t, f, finopsv1.OIDCConfig{DefaultRole: "operator"})
	if rr, _ := oidcSignIn(t, svc, f, false); sessionFrom(t, svc, rr) == nil || sessionFrom(t, svc, rr).Role != RoleOperator {
		t.Errorf("default role not granted")
	}
}

func TestOIDCClaimsAndRoles(t *testing.T) {
	var claims map[string]any
	_ = json.Unmarshal([]byte(`{"groups":["a","b",3],"single":"x","resource_access":{"costdeck":{"roles":["viewer"]}},"n":1}`), &claims)
	cases := map[string][]string{"groups": {"a", "b"}, "single": {"x"}, "resource_access.costdeck.roles": {"viewer"}, "missing": nil, "n": nil, "single.deeper": nil}
	for path, want := range cases {
		if got := ClaimStrings(claims, path); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("ClaimStrings(%s) = %v, want %v", path, got, want)
		}
	}
	cfg := finopsv1.OIDCConfig{GroupRoleMapping: map[string]string{"/ops": "operator", "admins": "admin"}}
	if r, _ := ResolveOIDCRole(cfg, []string{"ops"}, nil); r != RoleOperator {
		t.Errorf("a mapping with a leading slash should match the bare group: %s", r)
	}
	if r, _ := ResolveOIDCRole(cfg, []string{"/admins"}, []string{"viewer"}); r != RoleAdmin {
		t.Errorf("the most privileged match wins: %s", r)
	}
	if r, ok := ResolveOIDCRole(cfg, nil, []string{"default-roles-x"}); !ok || r != RoleViewer {
		t.Errorf("default: %s %v", r, ok)
	}
}

func TestOIDCOnTheLoginPage(t *testing.T) {
	f := newFakeEntra(t)
	svc := oidcService(t, f, finopsv1.OIDCConfig{DisplayName: "Keycloak"})
	rr := httptest.NewRecorder()
	svc.HandleConfig(rr, httptest.NewRequest(http.MethodGet, "/api/auth/config", nil))
	var got struct {
		OIDC struct {
			Enabled     bool   `json:"enabled"`
			DisplayName string `json:"displayName"`
		} `json:"oidc"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if !got.OIDC.Enabled || got.OIDC.DisplayName != "Keycloak" {
		t.Errorf("auth config = %s", rr.Body.String())
	}
	if msg, err := svc.OIDC.TestOIDC(t.Context(), finopsv1.OIDCConfig{IssuerURL: f.issuer(), ClientID: f.client}); err != nil || !strings.Contains(msg, "/authorize") {
		t.Errorf("test connection = %q, %v", msg, err)
	}
	if _, err := svc.OIDC.TestOIDC(t.Context(), finopsv1.OIDCConfig{IssuerURL: f.srv.URL + "/nowhere", ClientID: f.client}); err == nil {
		t.Error("an issuer without discovery passed the test")
	}
}
