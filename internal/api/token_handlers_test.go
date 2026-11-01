package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/migalsp/costdeck-operator/internal/auth"
)

type createdToken struct {
	Token string         `json:"token"`
	Info  auth.TokenInfo `json:"info"`
}

func TestTokenLifecycle(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	admin := &auth.Identity{Subject: "user:root", Name: "Root", Role: auth.RoleAdmin}

	if body := sendAs(t, server, admin, http.MethodGet, "/api/tokens", "").Body.String(); strings.TrimSpace(body) != "[]" {
		t.Errorf("tokens of a fresh install = %s, want []", body)
	}
	before := time.Now().UTC().Truncate(time.Second)
	rr := sendAs(t, server, admin, http.MethodPost, "/api/tokens", `{"name":"ci-pipeline","role":"operator","expiresInDays":30}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rr.Code, rr.Body.String())
	}
	created := decodeInto[createdToken](t, rr)
	info := created.Info
	if !strings.HasPrefix(created.Token, auth.TokenPrefix) || info.Name != "ci-pipeline" || info.Role != auth.RoleOperator || info.CreatedBy != "user:root" || info.Hash != "" {
		t.Errorf("created = %+v", created)
	}
	if info.ExpiresAt == nil || info.ExpiresAt.Sub(before) < 30*24*time.Hour || info.ExpiresAt.Sub(before) > 30*24*time.Hour+time.Minute {
		t.Errorf("expiresAt = %v, want 30 days from now", info.ExpiresAt)
	}
	id, ok := (&auth.Tokens{Client: server.Client}).Authenticate(context.Background(), created.Token)
	if !ok || id.Role != auth.RoleOperator || id.Subject != "token:ci-pipeline" {
		t.Errorf("the issued token does not sign in as an operator: %+v %v", id, ok)
	}

	list := sendAs(t, server, admin, http.MethodGet, "/api/tokens", "")
	if strings.Contains(list.Body.String(), created.Token) || strings.Contains(list.Body.String(), `"hash"`) {
		t.Errorf("the token list leaks the token or its hash: %s", list.Body.String())
	}
	if tokens := decodeInto[[]auth.TokenInfo](t, list); len(tokens) != 1 || tokens[0].Name != "ci-pipeline" || tokens[0].Role != auth.RoleOperator {
		t.Errorf("tokens = %+v", tokens)
	}

	if rr := sendAs(t, server, admin, http.MethodDelete, "/api/tokens/ci-pipeline", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", rr.Code, rr.Body.String())
	}
	if _, ok := (&auth.Tokens{Client: server.Client}).Authenticate(context.Background(), created.Token); ok {
		t.Error("a revoked token still signs in")
	}
	if rr := sendAs(t, server, admin, http.MethodDelete, "/api/tokens/ci-pipeline", ""); rr.Code != http.StatusNotFound {
		t.Errorf("revoking twice = %d, want 404", rr.Code)
	}
}

func TestCreateTokenValidatesTheRequest(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	if rr := send(t, server, http.MethodPost, "/api/tokens", `{"name":"taken","role":"viewer"}`); rr.Code != http.StatusCreated {
		t.Fatalf("seed = %d %s", rr.Code, rr.Body.String())
	}
	for body, want := range map[string]string{
		`{"name":"ci","role":"root"}`:                        "role must be viewer, operator or admin",
		`{"name":"ci","role":"viewer","expiresInDays":-1}`:   "expiresInDays must be between 0 (never) and 3650",
		`{"name":"ci","role":"viewer","expiresInDays":3651}`: "expiresInDays must be between 0 (never) and 3650",
		`{"name":"CI Pipeline","role":"viewer"}`:             "token names are lower-case letters, digits and dashes",
		`{"name":"taken","role":"admin"}`:                    `a token named "taken" already exists`,
		`{"name":`:                                           "unexpected EOF",
	} {
		rr := send(t, server, http.MethodPost, "/api/tokens", body)
		if rr.Code != http.StatusBadRequest || !strings.Contains(errorMessage(t, rr), want) {
			t.Errorf("%s = %d %s, want 400 %q", body, rr.Code, rr.Body.String(), want)
		}
	}
	tokens := decodeInto[[]auth.TokenInfo](t, send(t, server, http.MethodGet, "/api/tokens", ""))
	if len(tokens) != 1 || tokens[0].Role != auth.RoleViewer || tokens[0].ExpiresAt != nil {
		t.Errorf("tokens = %+v, want only the seeded viewer token that never expires", tokens)
	}
}

func TestRevokedTokenStopsWorkingAtOnce(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	t.Setenv("COSTDECK_AUTH_USER", "admin")
	t.Setenv("COSTDECK_AUTH_PASSWORD", "a-long-enough-password")
	server := buildMockServerWithK8s()
	svc, err := auth.NewService(context.Background(), server.Client)
	if err != nil {
		t.Fatal(err)
	}
	server.Auth = svc
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	adminToken, _, err := svc.Tokens.Create(context.Background(), "root", auth.RoleAdmin, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	rr := call(adminToken, http.MethodPost, "/api/tokens", `{"name":"script","role":"viewer"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rr.Code, rr.Body.String())
	}
	script := decodeInto[createdToken](t, rr).Token
	if rr := call(script, http.MethodGet, "/api/version", ""); rr.Code != http.StatusOK {
		t.Fatalf("a new token must work at once, got %d", rr.Code)
	}
	if rr := call(adminToken, http.MethodDelete, "/api/tokens/script", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", rr.Code, rr.Body.String())
	}
	if rr := call(script, http.MethodGet, "/api/version", ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("a revoked token = %d, want 401 at once rather than after the token cache expires", rr.Code)
	}
}
