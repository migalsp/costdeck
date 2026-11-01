package auth

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const fakeEntraSubject = "entra:oid-1"

func startEntraSignIn(t *testing.T, svc *Service, f *fakeEntra) (cookies []*http.Cookie, code, state string) {
	t.Helper()
	rr := httptest.NewRecorder()
	svc.Entra.HandleLogin(rr, httptest.NewRequest(http.MethodGet, "https://costdeck.example.com/api/auth/entra/login?return=/scaling", nil))
	if rr.Code != http.StatusFound {
		t.Fatalf("login = %d: %s", rr.Code, rr.Body.String())
	}
	code, state = f.authorize(rr.Header().Get("Location"))
	return rr.Result().Cookies(), code, state
}

func spaCallback(svc *Service, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, SPACallbackPath, strings.NewReader(body))
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	svc.Entra.HandleSPACallback(rr, req)
	return rr
}

func spaBody(code, state string) string {
	raw, _ := json.Marshal(map[string]string{"code": code, "state": state})
	return string(raw)
}

func transactionCleared(rr *httptest.ResponseRecorder) bool {
	for _, c := range rr.Result().Cookies() {
		if c.Name == txnCookie {
			return c.Value == "" && c.MaxAge < 0
		}
	}
	return false
}

func recordSignIns(svc *Service) *[]Identity {
	var seen []Identity
	svc.Entra.OnSignIn = func(_ context.Context, id Identity) { seen = append(seen, id) }
	return &seen
}

func TestEntraSPACallbackSignsInAndReturnsWhereTheUserStarted(t *testing.T) {
	f := newFakeEntra(t)
	f.claims = map[string]any{"groups": []string{"grp-finops"}}
	svc := entraService(t, f, map[string]string{"grp-finops": "operator"}, nil)
	signedIn := recordSignIns(svc)

	cookies, code, state := startEntraSignIn(t, svc, f)
	rr := spaCallback(svc, spaBody(code, state), cookies)
	if rr.Code != http.StatusOK {
		t.Fatalf("SPA callback = %d %s", rr.Code, rr.Body)
	}
	var got struct {
		User     Identity `json:"user"`
		ReturnTo string   `json:"returnTo"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ReturnTo != "/scaling" || got.User.Role != RoleOperator || got.User.Subject != fakeEntraSubject || got.User.Provider != "entra" {
		t.Errorf("SPA callback answer = %+v", got)
	}
	id := sessionFrom(t, svc, rr)
	if id == nil || id.Role != RoleOperator || id.Subject != fakeEntraSubject {
		t.Errorf("session = %+v", id)
	}
	if !transactionCleared(rr) {
		t.Error("the sign-in transaction cookie must be single use")
	}
	if len(*signedIn) != 1 || (*signedIn)[0].Subject != fakeEntraSubject {
		t.Errorf("OnSignIn calls = %+v", *signedIn)
	}
}

func TestEntraSPACallbackRefusals(t *testing.T) {
	f := newFakeEntra(t)
	svc := entraService(t, f, nil, nil)
	signedIn := recordSignIns(svc)
	other, err := NewSessions([]byte(strings.Repeat("o", 32)))
	if err != nil {
		t.Fatal(err)
	}
	txnValue := func(s *Sessions, expires time.Time) []*http.Cookie {
		token, err := s.Sign(oidcTxn{State: "st", Nonce: "n", Verifier: "v", ReturnTo: "/budgets", ExpiresAt: expires.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return []*http.Cookie{{Name: txnCookie, Value: token}}
	}

	cookies, code, _ := startEntraSignIn(t, svc, f)
	for name, c := range map[string]struct {
		body    string
		cookies []*http.Cookie
		status  int
		code    string
	}{
		"malformed body":                       {"{", cookies, http.StatusBadRequest, "invalid request"},
		"forged state":                         {spaBody(code, "forged"), cookies, http.StatusUnauthorized, "state_mismatch"},
		"empty state":                          {spaBody(code, ""), cookies, http.StatusUnauthorized, "state_mismatch"},
		"identity provider error":              {`{"error":"access_denied","error_description":"User cancelled"}`, cookies, http.StatusUnauthorized, "access_denied"},
		"no transaction cookie":                {spaBody(code, "st"), nil, http.StatusUnauthorized, "sign_in_expired"},
		"expired transaction":                  {spaBody(code, "st"), txnValue(svc.Sessions, time.Now().Add(-time.Second)), http.StatusUnauthorized, "sign_in_expired"},
		"transaction signed by another server": {spaBody(code, "st"), txnValue(other, time.Now().Add(time.Minute)), http.StatusUnauthorized, "sign_in_expired"},
	} {
		rr := spaCallback(svc, c.body, c.cookies)
		var body map[string]string
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		if rr.Code != c.status || body["error"] != c.code {
			t.Errorf("%s: %d %s, want %d %q", name, rr.Code, rr.Body, c.status, c.code)
		}
		if sessionFrom(t, svc, rr) != nil {
			t.Errorf("%s: a session was issued", name)
		}
	}
	if len(*signedIn) != 0 {
		t.Errorf("refused sign-ins were reported as sign-ins: %+v", *signedIn)
	}

	rr := spaCallback(svc, `{"error":"access_denied","error_description":"User cancelled"}`, cookies)
	var denied map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &denied)
	if denied["detail"] != "User cancelled" {
		t.Errorf("the provider's description is not passed on: %s", rr.Body)
	}
}

func TestEntraRejectsIDTokensFromTheWrongIssuerAudienceOrTime(t *testing.T) {
	f := newFakeEntra(t)
	for name, claims := range map[string]map[string]any{
		"issuer of another tenant": {"iss": "https://login.example/tenant-2/v2.0"},
		"issuer on another host":   {"iss": "https://login.evil.example/tenant-1/v2.0"},
		"audience of another app":  {"aud": "someone-else"},
		"expired":                  {"exp": time.Now().Add(-time.Hour).Unix()},
	} {
		f.claims = claims
		svc := entraService(t, f, nil, nil)
		rr := signIn(t, svc, f, false)
		if !strings.Contains(rr.Header().Get("Location"), "sso_error=invalid_token") || sessionFrom(t, svc, rr) != nil {
			t.Errorf("%s: callback -> %q, want invalid_token without a session", name, rr.Header().Get("Location"))
		}
	}
}

func TestEntraMultiTenantChecksTheIssuerAndTrustsOnlyGroupMappings(t *testing.T) {
	f := newFakeEntra(t)
	f.tenant = "organizations"
	userTenant := func(tid, issuerTenant string) map[string]any {
		return map[string]any{"tid": tid, "iss": f.srv.URL + "/" + issuerTenant + "/v2.0"}
	}
	with := func(claims map[string]any, extra map[string]any) map[string]any {
		maps.Copy(claims, extra)
		return claims
	}

	for name, c := range map[string]struct {
		claims  map[string]any
		role    Role
		ssoCode string
	}{
		"guest with a mapped group": {with(userTenant("tenant-a", "tenant-a"), map[string]any{"groups": []string{"grp-partners"}}), RoleOperator, ""},
		"app roles are ignored next to a mapped group": {
			with(userTenant("tenant-a", "tenant-a"), map[string]any{"groups": []string{"grp-partners"}, "roles": []string{"CostDeck.Admin"}}), RoleOperator, ""},
		"issuer of another tenant than tid": {
			with(userTenant("tenant-a", "tenant-b"), map[string]any{"groups": []string{"grp-partners"}}), "", "invalid_token"},
		"issuer on another host": {
			with(map[string]any{"tid": "tenant-a", "iss": "https://login.evil.example/tenant-a/v2.0"}, map[string]any{"groups": []string{"grp-partners"}}), "", "invalid_token"},
		"app role admin granted by a foreign tenant": {
			with(userTenant("tenant-a", "tenant-a"), map[string]any{"roles": []string{"CostDeck.Admin"}}), "", "user_not_provisioned"},
		"unmapped user is never auto-provisioned": {userTenant("tenant-a", "tenant-a"), "", "user_not_provisioned"},
	} {
		f.claims = c.claims
		svc := entraService(t, f, map[string]string{"grp-partners": "operator"}, nil)
		rr := signIn(t, svc, f, false)
		id := sessionFrom(t, svc, rr)
		if c.ssoCode != "" {
			if !strings.Contains(rr.Header().Get("Location"), "sso_error="+c.ssoCode) || id != nil {
				t.Errorf("%s: callback -> %q (session %+v), want %s", name, rr.Header().Get("Location"), id, c.ssoCode)
			}
			continue
		}
		if id == nil || id.Role != c.role {
			t.Errorf("%s: callback -> %q, session %+v, want role %s", name, rr.Header().Get("Location"), id, c.role)
		}
	}
}

func TestEntraLoginNeverReturnsToAnotherSite(t *testing.T) {
	f := newFakeEntra(t)
	svc := entraService(t, f, nil, nil)
	rr := httptest.NewRecorder()
	svc.Entra.HandleLogin(rr, httptest.NewRequest(http.MethodGet, "https://costdeck.example.com/api/auth/entra/login?return="+url.QueryEscape("https://evil.example/phish"), nil))
	code, state := f.authorize(rr.Header().Get("Location"))
	cb := httptest.NewRequest(http.MethodGet, CallbackPath+"?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), nil)
	for _, c := range rr.Result().Cookies() {
		cb.AddCookie(c)
	}
	out := httptest.NewRecorder()
	svc.Entra.HandleCallback(out, cb)
	if out.Code != http.StatusFound || out.Header().Get("Location") != "/" || sessionFrom(t, svc, out) == nil {
		t.Errorf("callback = %d -> %q, want a session and a redirect to /", out.Code, out.Header().Get("Location"))
	}
}

func TestEntraLoginWhenDisabledOrUnreachable(t *testing.T) {
	disabled := newTestService(t)
	rr := httptest.NewRecorder()
	disabled.Entra.HandleLogin(rr, httptest.NewRequest(http.MethodGet, "/api/auth/entra/login", nil))
	if loc, _ := url.Parse(rr.Header().Get("Location")); rr.Code != http.StatusFound || loc.Path != "/" || loc.Query().Get("sso_error") != "sso_disabled" {
		t.Errorf("login without Entra configured = %d -> %q", rr.Code, rr.Header().Get("Location"))
	}

	f := newFakeEntra(t)
	svc := entraService(t, f, nil, nil)
	f.srv.Close()
	rr = httptest.NewRecorder()
	svc.Entra.HandleLogin(rr, httptest.NewRequest(http.MethodGet, "/api/auth/entra/login?return=/budgets", nil))
	if loc, _ := url.Parse(rr.Header().Get("Location")); rr.Code != http.StatusFound || loc.Path != "/" || loc.Query().Get("sso_error") != "sso_unavailable" {
		t.Errorf("login with Entra unreachable = %d -> %q", rr.Code, rr.Header().Get("Location"))
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == txnCookie {
			t.Errorf("a sign-in that never started left a transaction cookie: %+v", c)
		}
	}
}
