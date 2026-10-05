package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func testSessions(t *testing.T) *Sessions {
	t.Helper()
	s, err := NewSessions([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionsRejectTamperingAndExpiry(t *testing.T) {
	s := testSessions(t)
	rr := httptest.NewRecorder()
	if err := s.Issue(rr, httptest.NewRequest(http.MethodGet, "/", nil), Identity{Subject: "u", Role: RoleViewer}); err != nil {
		t.Fatal(err)
	}
	cookie := rr.Result().Cookies()[0]

	read := func(value string) (*Identity, error) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: value})
		return s.Read(req)
	}
	if id, err := read(cookie.Value); err != nil || id.Role != RoleViewer {
		t.Fatalf("valid session rejected: %v", err)
	}

	// Escalating the role in the payload must break the signature.
	payload, sig, _ := strings.Cut(cookie.Value, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(payload)
	forged := strings.Replace(string(raw), `"viewer"`, `"admin"`, 1)
	if _, err := read(base64.RawURLEncoding.EncodeToString([]byte(forged)) + "." + sig); err == nil {
		t.Error("a tampered session was accepted")
	}

	expired, _ := s.Sign(Identity{Subject: "u", Role: RoleAdmin, ExpiresAt: time.Now().Add(-time.Minute).Unix()})
	if _, err := read(expired); err == nil {
		t.Error("an expired session was accepted")
	}
	if cookie.Secure {
		t.Error("over plain HTTP the cookie must not be Secure, or browsers drop it")
	}
}

func TestResolveRole(t *testing.T) {
	no := false
	cfg := finopsv1.EntraConfig{
		DefaultRole:      "operator",
		GroupRoleMapping: map[string]string{"g-admins": "admin", "g-ops": "owner", "g-readers": "reader"},
	}
	tests := []struct {
		name     string
		cfg      finopsv1.EntraConfig
		groups   []string
		roles    []string
		want     Role
		provided bool
	}{
		{"highest group wins", cfg, []string{"g-readers", "g-admins"}, nil, RoleAdmin, true},
		{"aliases are understood", cfg, []string{"g-ops"}, nil, RoleOperator, true},
		{"app roles count", cfg, nil, []string{"CostDeck.Admin"}, RoleAdmin, true},
		{"default role for unmapped users", cfg, []string{"unknown"}, nil, RoleOperator, true},
		{"viewer when no default is set", finopsv1.EntraConfig{}, nil, nil, RoleViewer, true},
		{"no auto-provisioning denies unmapped users", finopsv1.EntraConfig{AutoProvision: &no}, []string{"x"}, nil, "", false},
		{"no auto-provisioning still admits mapped users", finopsv1.EntraConfig{AutoProvision: &no, GroupRoleMapping: cfg.GroupRoleMapping}, []string{"g-readers"}, nil, RoleViewer, true},
	}
	for _, tt := range tests {
		got, ok := ResolveRole(tt.cfg, tt.groups, tt.roles)
		if got != tt.want || ok != tt.provided {
			t.Errorf("%s: ResolveRole() = %q, %v; want %q, %v", tt.name, got, ok, tt.want, tt.provided)
		}
	}
}

func newTestService(t *testing.T, objs ...client.Object) *Service {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	s := testSessions(t)
	return &Service{Client: c, Sessions: s, Entra: &Entra{Client: c, Sessions: s}, localUser: "admin", localPassword: "pw"}
}

func TestMiddlewareAndRoles(t *testing.T) {
	svc := newTestService(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/thing", Require(RoleViewer, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	mux.HandleFunc("POST /api/thing", Require(RoleOperator, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	mux.HandleFunc("GET /api/auth/config", svc.HandleConfig)
	h := svc.Middleware(mux)

	do := func(method, path string, id *Identity) int {
		req := httptest.NewRequest(method, path, nil)
		if id != nil {
			token, _ := svc.Sessions.Sign(Identity{Subject: id.Subject, Role: id.Role, ExpiresAt: time.Now().Add(time.Hour).Unix()})
			req.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}

	if code := do(http.MethodGet, "/api/thing", nil); code != http.StatusUnauthorized {
		t.Errorf("anonymous API call = %d, want 401", code)
	}
	if code := do(http.MethodGet, "/api/auth/config", nil); code != http.StatusOK {
		t.Errorf("public endpoint = %d, want 200", code)
	}
	viewer := &Identity{Subject: "v", Role: RoleViewer}
	if code := do(http.MethodGet, "/api/thing", viewer); code != http.StatusOK {
		t.Errorf("viewer GET = %d, want 200", code)
	}
	if code := do(http.MethodPost, "/api/thing", viewer); code != http.StatusForbidden {
		t.Errorf("viewer POST = %d, want 403", code)
	}
	if code := do(http.MethodPost, "/api/thing", &Identity{Subject: "o", Role: RoleOperator}); code != http.StatusOK {
		t.Errorf("operator POST = %d, want 200", code)
	}

	// With no sign-in method configured, everything runs as an anonymous admin.
	svc.localUser, svc.localPassword = "", ""
	if code := do(http.MethodPost, "/api/thing", nil); code != http.StatusOK {
		t.Errorf("auth disabled POST = %d, want 200", code)
	}
}

func TestLocalLogin(t *testing.T) {
	svc := newTestService(t)
	login := func(user, pass string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(string(body)))
		req.RemoteAddr = "10.0.0.1:1234"
		rr := httptest.NewRecorder()
		svc.HandleLogin(rr, req)
		return rr
	}

	if rr := login("admin", "pw"); rr.Code != http.StatusOK || len(rr.Result().Cookies()) == 0 {
		t.Fatalf("valid login = %d", rr.Code)
	}
	if rr := login("admin", "nope"); rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", rr.Code)
	}
	var last int
	for range 6 {
		last = login("admin", "nope").Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("brute force attempts end with %d, want 429", last)
	}
}

// fakeEntra is a minimal OpenID provider shaped like Entra ID.
type fakeEntra struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	tenant string
	client string
	secret string

	mu     sync.Mutex
	codes  map[string]fakeCode
	claims map[string]any // extra claims for the next token
}

type fakeCode struct{ nonce, challenge string }

func newFakeEntra(t *testing.T) *fakeEntra {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeEntra{t: t, key: key, tenant: "tenant-1", client: "client-1", secret: "s3cr3t", codes: map[string]fakeCode{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEntra) issuer() string { return f.srv.URL + "/" + f.tenant + "/v2.0" }

func (f *fakeEntra) serve(w http.ResponseWriter, r *http.Request) {
	base := "/" + f.tenant
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case base + "/v2.0/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.issuer(),
			"authorization_endpoint":                f.srv.URL + base + "/oauth2/v2.0/authorize",
			"token_endpoint":                        f.srv.URL + base + "/oauth2/v2.0/token",
			"jwks_uri":                              f.srv.URL + base + "/discovery/v2.0/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	case base + "/discovery/v2.0/keys":
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	case base + "/oauth2/v2.0/token":
		_ = r.ParseForm()
		user, pass, _ := r.BasicAuth()
		if user == "" {
			user, pass = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		if user != f.client || pass != f.secret {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		code, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		extra := f.claims
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != code.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		claims := map[string]any{
			"iss": f.issuer(), "aud": f.client, "sub": "s1", "oid": "oid-1", "tid": f.tenant,
			"name": "Ada Lovelace", "preferred_username": "ada@example.com", "nonce": code.nonce,
			"iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		}
		maps.Copy(claims, extra)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": f.sign(claims)})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeEntra) sign(claims map[string]any) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		f.t.Fatal(err)
	}
	payload, _ := json.Marshal(claims)
	jws, err := signer.Sign(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	out, _ := jws.CompactSerialize()
	return out
}

// authorize plays the browser + IdP: it reads the authorize redirect and mints a code.
func (f *fakeEntra) authorize(location string) (code, state string) {
	u, _ := url.Parse(location)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		f.t.Fatalf("authorize request without PKCE: %s", location)
	}
	code = "code-" + q.Get("state")
	f.mu.Lock()
	f.codes[code] = fakeCode{nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
	f.mu.Unlock()
	return code, q.Get("state")
}

func entraService(t *testing.T, f *fakeEntra, mapping map[string]string, autoProvision *bool) *Service {
	return newTestService(t,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "entra", Namespace: "costdeck"}, Data: map[string][]byte{SecretKeyClientSecret: []byte(f.secret)}},
		&finopsv1.CostDeckConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
			Spec: finopsv1.CostDeckConfigSpec{Auth: finopsv1.AuthConfig{Entra: &finopsv1.EntraConfig{
				Enabled: true, TenantID: f.tenant, ClientID: f.client, ClientSecretRef: "entra",
				AuthorityHost: f.srv.URL, GroupRoleMapping: mapping, AutoProvision: autoProvision,
			}}},
		})
}

// signIn runs login -> authorize -> callback and returns the callback response.
func signIn(t *testing.T, svc *Service, f *fakeEntra, tamperState bool) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	svc.Entra.HandleLogin(rr, httptest.NewRequest(http.MethodGet, "https://costdeck.example.com/api/auth/entra/login?return=/scaling", nil))
	if rr.Code != http.StatusFound {
		t.Fatalf("login = %d: %s", rr.Code, rr.Body.String())
	}
	location := rr.Header().Get("Location")
	if !strings.Contains(location, url.QueryEscape("https://costdeck.example.com"+CallbackPath)) {
		t.Errorf("redirect_uri was not derived from the request host: %s", location)
	}
	code, state := f.authorize(location)
	if tamperState {
		state = "forged"
	}

	cb := httptest.NewRequest(http.MethodGet, CallbackPath+"?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), nil)
	for _, c := range rr.Result().Cookies() {
		cb.AddCookie(c)
	}
	out := httptest.NewRecorder()
	svc.Entra.HandleCallback(out, cb)
	return out
}

func sessionFrom(t *testing.T, svc *Service, rr *httptest.ResponseRecorder) *Identity {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rr.Result().Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			req.AddCookie(c)
		}
	}
	id, err := svc.Sessions.Read(req)
	if err != nil {
		return nil
	}
	return id
}

func TestEntraSignInMapsGroupsToRoles(t *testing.T) {
	f := newFakeEntra(t)
	f.claims = map[string]any{"groups": []string{"grp-finops"}}
	svc := entraService(t, f, map[string]string{"grp-finops": "operator"}, nil)

	rr := signIn(t, svc, f, false)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/scaling" {
		t.Fatalf("callback = %d -> %q", rr.Code, rr.Header().Get("Location"))
	}
	id := sessionFrom(t, svc, rr)
	if id == nil || id.Role != RoleOperator || id.Email != "ada@example.com" || id.Subject != "entra:oid-1" {
		t.Fatalf("session identity = %+v", id)
	}
}

func TestEntraRejectsForgedStateAndUnprovisionedUsers(t *testing.T) {
	f := newFakeEntra(t)
	svc := entraService(t, f, nil, nil)
	rr := signIn(t, svc, f, true)
	if !strings.Contains(rr.Header().Get("Location"), "sso_error=state_mismatch") || sessionFrom(t, svc, rr) != nil {
		t.Errorf("a forged state must fail without a session, got %q", rr.Header().Get("Location"))
	}

	no := false
	svc = entraService(t, f, map[string]string{"grp-admins": "admin"}, &no)
	rr = signIn(t, svc, f, false)
	if !strings.Contains(rr.Header().Get("Location"), "sso_error=user_not_provisioned") || sessionFrom(t, svc, rr) != nil {
		t.Errorf("an unmapped user must be refused when auto-provisioning is off, got %q", rr.Header().Get("Location"))
	}
}

func TestEntraRejectsTokenWithWrongNonce(t *testing.T) {
	f := newFakeEntra(t)
	f.claims = map[string]any{"nonce": "replayed"}
	svc := entraService(t, f, nil, nil)
	rr := signIn(t, svc, f, false)
	if !strings.Contains(rr.Header().Get("Location"), "sso_error=invalid_token") || sessionFrom(t, svc, rr) != nil {
		t.Errorf("a token with a foreign nonce must be rejected, got %q", rr.Header().Get("Location"))
	}
}

func TestEntraTestConnection(t *testing.T) {
	f := newFakeEntra(t)
	svc := entraService(t, f, nil, nil)
	cfg := finopsv1.EntraConfig{TenantID: f.tenant, ClientID: f.client, AuthorityHost: f.srv.URL}
	if _, err := svc.Entra.TestEntra(context.Background(), cfg, f.secret); err == nil {
		// The fake token endpoint has no client_credentials grant; a real tenant would
		// answer with a token. Only the wrong-secret branch is asserted below.
		t.Log("client credentials accepted")
	}
	if _, err := svc.Entra.TestEntra(context.Background(), cfg, "wrong"); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("a wrong client secret must be reported, got %v", err)
	}
}

func TestSafeReturnPath(t *testing.T) {
	for in, want := range map[string]string{
		"/scaling": "/scaling", "": "/", "https://evil.example": "/", "//evil.example": "/",
		`/\evil`: "/", CallbackPath: "/",
	} {
		if got := safeReturnPath(in); got != want {
			t.Errorf("safeReturnPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTokensLifecycle(t *testing.T) {
	svc := newTestService(t)
	svc.Tokens = &Tokens{Client: svc.Client}
	ctx := context.Background()

	token, info, err := svc.Tokens.Create(ctx, "claude-desktop", RoleOperator, 0, "local:admin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, TokenPrefix) || info.Hash != "" {
		t.Fatalf("token = %q, info = %+v", token, info)
	}
	if _, _, err := svc.Tokens.Create(ctx, "claude-desktop", RoleViewer, 0, ""); err == nil {
		t.Error("duplicate token names must be rejected")
	}
	if _, _, err := svc.Tokens.Create(ctx, "Bad Name", RoleViewer, 0, ""); err == nil {
		t.Error("invalid token names must be rejected")
	}

	id, ok := svc.Tokens.Authenticate(ctx, token)
	if !ok || id.Role != RoleOperator || id.Subject != "token:claude-desktop" {
		t.Fatalf("Authenticate() = %+v, %v", id, ok)
	}
	if _, ok := svc.Tokens.Authenticate(ctx, token+"x"); ok {
		t.Error("a wrong token was accepted")
	}

	list, err := svc.Tokens.List(ctx)
	if err != nil || len(list) != 1 || list[0].Hash != "" {
		t.Errorf("List() = %+v, %v (hashes must never be listed)", list, err)
	}

	if err := svc.Tokens.Delete(ctx, "claude-desktop"); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.Tokens.Authenticate(ctx, token); ok {
		t.Error("a revoked token was still accepted")
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	svc := newTestService(t)
	svc.Tokens = &Tokens{Client: svc.Client}
	token, _, err := svc.Tokens.Create(context.Background(), "ci", RoleViewer, time.Nanosecond, "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, ok := svc.Tokens.Authenticate(context.Background(), token); ok {
		t.Error("an expired token was accepted")
	}
}

func TestMiddlewareAcceptsBearerTokens(t *testing.T) {
	svc := newTestService(t)
	svc.Tokens = &Tokens{Client: svc.Client}
	token, _, err := svc.Tokens.Create(context.Background(), "mcp", RoleViewer, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(FromContext(r.Context()))
	}))
	do := func(path, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	if rr := do("/mcp", "Bearer "+token); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"role":"viewer"`) {
		t.Errorf("valid token on /mcp = %d %s", rr.Code, rr.Body.String())
	}
	if rr := do("/mcp", ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("/mcp without credentials = %d, want 401", rr.Code)
	}
	if rr := do("/api/scaling/groups", "Bearer cdk_forged"); rr.Code != http.StatusUnauthorized {
		t.Errorf("forged token = %d, want 401", rr.Code)
	}
}
