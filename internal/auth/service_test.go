package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

const annaSession = "user:anna"

var remoteClients atomic.Int32

func uniqueRemoteAddr() string {
	n := remoteClients.Add(1)
	return fmt.Sprintf("10.1.%d.%d:4000", n/250, n%250)
}

func postJSON(h http.Handler, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var raw string
	if s, ok := body.(string); ok {
		raw = s
	} else {
		b, _ := json.Marshal(body)
		raw = string(b)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(raw))
	req.RemoteAddr = uniqueRemoteAddr()
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func getWith(h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func errorOf(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response %d is not a JSON object: %s", rr.Code, rr.Body)
	}
	return body["error"]
}

func sessionCookieOf(rr *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	return nil
}

func createdBeforeTheSession(t *testing.T, users *Users, name string) {
	t.Helper()
	err := users.write(context.Background(), name, func(existing map[string]User) (*User, error) {
		user := existing[name]
		user.PasswordChangedAt = user.PasswordChangedAt.Add(-2 * time.Minute)
		user.CreatedAt = user.PasswordChangedAt
		return &user, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func olderSession(t *testing.T, svc *Service, id Identity) *http.Cookie {
	t.Helper()
	id.IssuedAt = time.Now().Add(-time.Minute).Unix()
	id.ExpiresAt = time.Now().Add(time.Hour).Unix()
	token, err := svc.Sessions.Sign(id)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: SessionCookie, Value: token}
}

func protectedMux(svc *Service) http.Handler {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	mux.HandleFunc("GET /api/thing", Require(RoleViewer, ok))
	mux.HandleFunc("POST /api/settings", Require(RoleAdmin, ok))
	mux.HandleFunc("GET /api/auth/me", svc.HandleMe)
	mux.HandleFunc("POST /api/auth/password", svc.HandleChangePassword)
	mux.HandleFunc("POST /api/login", svc.HandleLogin)
	mux.HandleFunc("GET /api/auth/config", svc.HandleConfig)
	return svc.Middleware(mux)
}

func TestManagedSessionsFollowTheAccount(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	h := protectedMux(svc)
	if _, err := svc.Users.Create(ctx, User{Username: "anna", Role: RoleOperator}, "anna's long password", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Users.Create(ctx, User{Username: "bob", Role: RoleViewer}, "bob's long password", false, ""); err != nil {
		t.Fatal(err)
	}
	createdBeforeTheSession(t, svc.Users, "anna")
	createdBeforeTheSession(t, svc.Users, "bob")

	claimsAdmin := olderSession(t, svc, Identity{Subject: annaSession, Name: "anna", Role: RoleAdmin, Provider: "local"})
	rr := getWith(h, "/api/auth/me", claimsAdmin)
	var me Identity
	_ = json.Unmarshal(rr.Body.Bytes(), &me)
	if rr.Code != http.StatusOK || me.Role != RoleOperator {
		t.Errorf("me = %d %s, want the account's current role operator, not the role in the cookie", rr.Code, rr.Body)
	}
	if rr := postJSON(h, "/api/settings", map[string]string{}, claimsAdmin); rr.Code != http.StatusForbidden {
		t.Errorf("an admin cookie of a user demoted to operator reached an admin route: %d", rr.Code)
	}

	if err := svc.Users.SetPassword(ctx, "anna", "reset by an admin!", true); err != nil {
		t.Fatal(err)
	}
	rr = getWith(h, "/api/thing", claimsAdmin)
	if rr.Code != http.StatusUnauthorized || !strings.Contains(errorOf(t, rr), "the password was changed") {
		t.Errorf("a session older than a password reset = %d %s, want 401", rr.Code, rr.Body)
	}
	if c := sessionCookieOf(rr); c == nil || c.MaxAge >= 0 {
		t.Errorf("an ended session must clear the cookie, got %+v", c)
	}

	bob := olderSession(t, svc, Identity{Subject: "user:bob", Role: RoleViewer, Provider: "local"})
	if rr := getWith(h, "/api/thing", bob); rr.Code != http.StatusOK {
		t.Fatalf("bob's session = %d %s", rr.Code, rr.Body)
	}
	if err := svc.Users.Delete(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	rr = getWith(h, "/api/thing", bob)
	if rr.Code != http.StatusUnauthorized || !strings.Contains(errorOf(t, rr), ErrUserNotFound.Error()) {
		t.Errorf("a deleted user's session = %d %s, want 401", rr.Code, rr.Body)
	}
}

func TestPasswordChangeEndsTheUsersOtherSessions(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	h := protectedMux(svc)
	if _, err := svc.Users.Create(ctx, User{Username: "anna", Role: RoleViewer}, "first long password", false, ""); err != nil {
		t.Fatal(err)
	}
	createdBeforeTheSession(t, svc.Users, "anna")
	otherBrowser := olderSession(t, svc, Identity{Subject: annaSession, Role: RoleViewer, Provider: "local"})
	thisBrowser := olderSession(t, svc, Identity{Subject: annaSession, Role: RoleViewer, Provider: "local"})

	rr := postJSON(h, "/api/auth/password", map[string]string{"currentPassword": "first long password", "newPassword": "first long password"}, thisBrowser)
	if rr.Code != http.StatusBadRequest || !strings.Contains(errorOf(t, rr), "different from the current one") {
		t.Errorf("reusing the password = %d %s", rr.Code, rr.Body)
	}
	rr = postJSON(h, "/api/auth/password", map[string]string{"currentPassword": "first long password", "newPassword": "short"}, thisBrowser)
	if msg := errorOf(t, rr); rr.Code != http.StatusBadRequest || msg != fmt.Sprintf("passwords have at least %d characters", MinPasswordLength) {
		t.Errorf("a too short password = %d %q, want 400 with the rule and no error prefix", rr.Code, msg)
	}
	rr = postJSON(h, "/api/auth/password", "{", thisBrowser)
	if rr.Code != http.StatusBadRequest || errorOf(t, rr) != "Invalid request body" {
		t.Errorf("a malformed body = %d %s", rr.Code, rr.Body)
	}

	rr = postJSON(h, "/api/auth/password", map[string]string{"currentPassword": "first long password", "newPassword": "second long password"}, thisBrowser)
	if rr.Code != http.StatusOK {
		t.Fatalf("change = %d %s", rr.Code, rr.Body)
	}
	fresh := sessionCookieOf(rr)
	if fresh == nil || fresh.Value == "" {
		t.Fatal("the browser that changed the password gets a new session")
	}
	if rr := getWith(h, "/api/thing", otherBrowser); rr.Code != http.StatusUnauthorized {
		t.Errorf("a session from before the change = %d, want 401", rr.Code)
	}
	if rr := getWith(h, "/api/thing", fresh); rr.Code != http.StatusOK {
		t.Errorf("the re-issued session = %d %s", rr.Code, rr.Body)
	}
	if _, err := svc.Users.Authenticate(ctx, "anna", "second long password"); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
}

func TestPasswordChangeIsOnlyForManagedUsers(t *testing.T) {
	svc := newTestService(t)
	for name, id := range map[string]*Identity{
		"built-in admin": {Subject: "local:admin", Role: RoleAdmin, Provider: "local"},
		"single sign-on": {Subject: fakeEntraSubject, Role: RoleOperator, Provider: "entra"},
		"API token":      {Subject: "token:ci", Role: RoleAdmin, Provider: "token"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/password", strings.NewReader(`{"currentPassword":"pw","newPassword":"a new long password"}`))
		req = req.WithContext(WithIdentity(req.Context(), id))
		rr := httptest.NewRecorder()
		svc.HandleChangePassword(rr, req)
		if rr.Code != http.StatusBadRequest || !strings.Contains(errorOf(t, rr), "Only local users change their password here") {
			t.Errorf("%s: %d %s", name, rr.Code, rr.Body)
		}
	}
}

func TestHandleLoginForManagedUsers(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	h := protectedMux(svc)
	svc.localUser, svc.localPassword = "", ""

	rr := postJSON(h, "/api/login", map[string]string{"username": "anna", "password": "anna's long password"})
	if rr.Code != http.StatusForbidden || !strings.Contains(errorOf(t, rr), "not configured") {
		t.Errorf("no built-in admin and no users = %d %s, want 403", rr.Code, rr.Body)
	}

	if _, err := svc.Users.Create(ctx, User{Username: "anna", Role: RoleOperator}, "anna's long password", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Users.Create(ctx, User{Username: "carl", Role: RoleViewer}, "carl's long password", false, ""); err != nil {
		t.Fatal(err)
	}
	disabled := true
	if _, err := svc.Users.Update(ctx, "carl", UserPatch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}

	rr = postJSON(h, "/api/login", map[string]string{"username": "anna", "password": "anna's long password"})
	if rr.Code != http.StatusOK {
		t.Fatalf("login of a managed user without a built-in admin = %d %s", rr.Code, rr.Body)
	}
	if id := sessionFrom(t, svc, rr); id == nil || id.Subject != annaSession || id.Role != RoleOperator {
		t.Errorf("session = %+v", id)
	}
	signIns, _ := svc.SignIns.List(ctx)
	if len(signIns) != 1 || signIns[0].Subject != annaSession {
		t.Errorf("the sign-in was not recorded: %+v", signIns)
	}

	for name, c := range map[string]struct {
		body any
		code int
		msg  string
	}{
		"disabled account": {map[string]string{"username": "carl", "password": "carl's long password"}, http.StatusForbidden, "This account is disabled. Ask an administrator."},
		"wrong password":   {map[string]string{"username": "anna", "password": "not her password"}, http.StatusUnauthorized, "Invalid credentials"},
		"unknown user":     {map[string]string{"username": "nobody", "password": "anna's long password"}, http.StatusUnauthorized, "Invalid credentials"},
		"malformed body":   {"{\"username\":", http.StatusBadRequest, "Invalid request body"},
	} {
		rr := postJSON(h, "/api/login", c.body)
		if rr.Code != c.code || errorOf(t, rr) != c.msg || sessionCookieOf(rr) != nil {
			t.Errorf("%s: %d %s, want %d %q and no session", name, rr.Code, rr.Body, c.code, c.msg)
		}
	}
}

func TestHiddenPasswordFormKeepsTheBuiltinAdminAsBreakGlass(t *testing.T) {
	ctx := context.Background()
	withSSO := newTestService(t, &finopsv1.CostDeckConfig{
		Name: "default", Namespace: "costdeck",
		Spec: finopsv1.CostDeckConfigSpec{Auth: finopsv1.AuthConfig{
			DisableLocalLogin: true,
			Entra:             &finopsv1.EntraConfig{Enabled: true, TenantID: "tenant-1", ClientID: "client-1"},
		}},
	})
	if _, err := withSSO.Users.Create(ctx, User{Username: "anna", Role: RoleViewer}, "anna's long password", false, ""); err != nil {
		t.Fatal(err)
	}
	h := protectedMux(withSSO)

	rr := postJSON(h, "/api/login", map[string]string{"username": "anna", "password": "anna's long password"})
	if rr.Code != http.StatusForbidden || !strings.Contains(errorOf(t, rr), "turned off") || sessionCookieOf(rr) != nil {
		t.Errorf("a managed user with the password form hidden = %d %s, want 403", rr.Code, rr.Body)
	}
	if rr := postJSON(h, "/api/login", map[string]string{"username": "admin", "password": "pw"}); rr.Code != http.StatusOK || sessionCookieOf(rr) == nil {
		t.Errorf("the built-in admin must stay usable as break-glass access: %d %s", rr.Code, rr.Body)
	}
	var cfg struct {
		LocalLogin bool `json:"localLogin"`
	}
	_ = json.Unmarshal(getWith(h, "/api/auth/config").Body.Bytes(), &cfg)
	if cfg.LocalLogin {
		t.Error("the login page still offers the hidden password form")
	}

	withoutSSO := newTestService(t, &finopsv1.CostDeckConfig{
		Name: "default", Namespace: "costdeck",
		Spec: finopsv1.CostDeckConfigSpec{Auth: finopsv1.AuthConfig{DisableLocalLogin: true}},
	})
	if _, err := withoutSSO.Users.Create(ctx, User{Username: "anna", Role: RoleViewer}, "anna's long password", false, ""); err != nil {
		t.Fatal(err)
	}
	if rr := postJSON(protectedMux(withoutSSO), "/api/login", map[string]string{"username": "anna", "password": "anna's long password"}); rr.Code != http.StatusOK {
		t.Errorf("without single sign-on the setting must not lock users out: %d %s", rr.Code, rr.Body)
	}
}

func TestHandleLoginWhileAuthIsDisabledIssuesNoSession(t *testing.T) {
	svc := newTestService(t)
	svc.disabled = true
	rr := postJSON(protectedMux(svc), "/api/login", map[string]string{"username": "admin", "password": "wrong"})
	if rr.Code != http.StatusOK || sessionCookieOf(rr) != nil {
		t.Errorf("login with authentication disabled = %d, cookie %+v", rr.Code, sessionCookieOf(rr))
	}
}

func TestHandleLogoutClearsTheSessionCookie(t *testing.T) {
	svc := newTestService(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	svc.HandleLogout(rr, req)

	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != `{"status":"ok"}` {
		t.Errorf("logout = %d %s", rr.Code, rr.Body)
	}
	c := sessionCookieOf(rr)
	if c == nil || c.Value != "" || c.MaxAge >= 0 || !c.Secure || !c.HttpOnly || c.Path != "/" {
		t.Errorf("logout cookie = %+v, want the session cookie expired with the same flags", c)
	}
	if !PublicPath("/api/logout") {
		t.Error("logout must be reachable with an already expired session")
	}
}

func TestBuiltinAdmin(t *testing.T) {
	svc := newTestService(t)
	if got := svc.BuiltinAdmin(); got != "admin" {
		t.Errorf("BuiltinAdmin() = %q, want admin", got)
	}
	svc.localPassword = ""
	if got := svc.BuiltinAdmin(); got != "" {
		t.Errorf("an account without a password is no account, BuiltinAdmin() = %q", got)
	}

	c := bootstrapClient(t)
	t.Setenv(envAuthUser, "ops")
	t.Setenv(envAuthPassword, "a long helm password")
	fromHelm, err := NewService(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if got := fromHelm.BuiltinAdmin(); got != "ops" {
		t.Errorf("BuiltinAdmin() = %q, want the COSTDECK_AUTH_USER", got)
	}
	if _, err := fromHelm.Users.Create(context.Background(), User{Username: "OPS", Role: RoleViewer}, "a long user password", false, ""); !errors.Is(err, ErrInvalidUser) {
		t.Errorf("a managed user took the built-in admin's name: %v", err)
	}
}

func TestMiddlewareDoesNotFallBackFromABadTokenToTheCookie(t *testing.T) {
	svc := newTestService(t)
	svc.Tokens = &Tokens{Client: svc.Client}
	h := protectedMux(svc)
	admin := olderSession(t, svc, Identity{Subject: "local:admin", Role: RoleAdmin, Provider: "local"})

	req := httptest.NewRequest(http.MethodGet, "/api/thing", nil)
	req.Header.Set("Authorization", "Bearer cdk_revoked")
	req.AddCookie(admin)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized || rr.Header().Get("WWW-Authenticate") != `Bearer realm="costdeck"` {
		t.Errorf("a rejected bearer token with a valid cookie = %d (WWW-Authenticate %q), want 401", rr.Code, rr.Header().Get("WWW-Authenticate"))
	}
}

func TestCrossSiteGuardCoversEveryWriteMethod(t *testing.T) {
	svc := newTestService(t)
	svc.Tokens = &Tokens{Client: svc.Client}
	token, _, err := svc.Tokens.Create(context.Background(), "ci", RoleAdmin, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	admin := olderSession(t, svc, Identity{Subject: "local:admin", Role: RoleAdmin, Provider: "local"})

	for name, c := range map[string]struct {
		method  string
		headers map[string]string
		bearer  bool
		want    int
	}{
		"DELETE from another site":                    {http.MethodDelete, map[string]string{"Sec-Fetch-Site": "cross-site"}, false, http.StatusForbidden},
		"PUT from a sibling subdomain":                {http.MethodPut, map[string]string{"Sec-Fetch-Site": "same-site"}, false, http.StatusForbidden},
		"PATCH with a foreign Origin":                 {http.MethodPatch, map[string]string{"Origin": "https://evil.example"}, false, http.StatusForbidden},
		"POST from a sandboxed frame (Origin null)":   {http.MethodPost, map[string]string{"Origin": "null"}, false, http.StatusForbidden},
		"POST with a port-mismatched Origin":          {http.MethodPost, map[string]string{"Origin": "http://costdeck.example.com:8443"}, false, http.StatusForbidden},
		"POST the user started (Sec-Fetch-Site none)": {http.MethodPost, map[string]string{"Sec-Fetch-Site": "none"}, false, http.StatusOK},
		"cross-site POST with an API token":           {http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site"}, true, http.StatusOK},
	} {
		req := httptest.NewRequest(c.method, "http://costdeck.example.com/api/thing", nil)
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		if c.bearer {
			req.Header.Set("Authorization", "Bearer "+token)
		} else {
			req.AddCookie(admin)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != c.want {
			t.Errorf("%s: %d, want %d", name, rr.Code, c.want)
		}
	}
}
