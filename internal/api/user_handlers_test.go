package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/migalsp/costdeck-operator/internal/auth"
)

func TestUserManagement(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	call := func(method, path, body string, as *auth.Identity) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if as != nil {
			req = req.WithContext(auth.WithIdentity(req.Context(), as))
		}
		rr := httptest.NewRecorder()
		server.routes().ServeHTTP(rr, req)
		return rr
	}
	admin := &auth.Identity{Subject: "user:root", Role: auth.RoleAdmin}

	rr := call(http.MethodPost, "/api/users", `{"username":"anna","name":"Anna","role":"operator"}`, admin)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rr.Code, rr.Body)
	}
	var created struct {
		User     auth.User `json:"user"`
		Password string    `json:"password"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if len(created.Password) < auth.MinPasswordLength || !created.User.MustChangePassword || created.User.CreatedBy != "user:root" {
		t.Errorf("a generated password is returned once and must be changed: %+v", created)
	}
	if _, err := server.users().Authenticate(context.Background(), "anna", created.Password); err != nil {
		t.Errorf("the generated password signs in: %v", err)
	}
	if rr := call(http.MethodPost, "/api/users", `{"username":"anna","role":"viewer"}`, admin); rr.Code != http.StatusBadRequest {
		t.Errorf("a taken name = %d", rr.Code)
	}
	if rr := call(http.MethodPost, "/api/users", `{"username":"bob","role":"viewer","password":"short"}`, admin); rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "at least 12") {
		t.Errorf("a short password = %d %s", rr.Code, rr.Body)
	}

	if rr := call(http.MethodGet, "/api/users", "", admin); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "hash") || !strings.Contains(rr.Body.String(), `"anna"`) {
		t.Errorf("list = %d %s", rr.Code, rr.Body)
	}
	if rr := call(http.MethodPatch, "/api/users/anna", `{"role":"viewer","disabled":true}`, admin); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"disabled":true`) {
		t.Errorf("patch = %d %s", rr.Code, rr.Body)
	}
	rr = call(http.MethodPost, "/api/users/anna/password", `{}`, admin)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"password"`) {
		t.Errorf("reset = %d %s", rr.Code, rr.Body)
	}
	if rr := call(http.MethodPost, "/api/users/nobody/password", `{}`, admin); rr.Code != http.StatusNotFound {
		t.Errorf("reset of an unknown user = %d", rr.Code)
	}

	// Nobody locks themselves out.
	self := &auth.Identity{Subject: "user:anna", Role: auth.RoleAdmin}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/users/anna", `{"disabled":true}`},
		{http.MethodPatch, "/api/users/anna", `{"role":"viewer"}`},
		{http.MethodDelete, "/api/users/anna", ``},
	} {
		if rr := call(c.method, c.path, c.body, self); rr.Code != http.StatusBadRequest {
			t.Errorf("%s %s %s on oneself = %d", c.method, c.path, c.body, rr.Code)
		}
	}
	if rr := call(http.MethodGet, "/api/users", "", &auth.Identity{Subject: "v", Role: auth.RoleOperator}); rr.Code != http.StatusForbidden {
		t.Errorf("only admins manage users: %d", rr.Code)
	}
	if rr := call(http.MethodDelete, "/api/users/anna", "", admin); rr.Code != http.StatusNoContent {
		t.Errorf("delete = %d %s", rr.Code, rr.Body)
	}
}
