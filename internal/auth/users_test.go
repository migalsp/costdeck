package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUsersLifecycle(t *testing.T) {
	ctx := context.Background()
	users := newTestService(t).Users

	if _, err := users.Create(ctx, User{Username: "Anna", Role: RoleOperator}, "correct horse battery", true, "local:admin"); err != nil {
		t.Fatal(err)
	}
	for name, u := range map[string]User{
		"duplicate":      {Username: "anna", Role: RoleViewer},
		"built-in admin": {Username: "admin", Role: RoleViewer},
		"bad name":       {Username: "a b", Role: RoleViewer},
		"bad role":       {Username: "bob", Role: "root"},
		"bad email":      {Username: "bob", Role: RoleViewer, Email: "not-an-email"},
	} {
		if _, err := users.Create(ctx, u, "correct horse battery", false, ""); !errors.Is(err, ErrInvalidUser) {
			t.Errorf("%s: err = %v, want ErrInvalidUser", name, err)
		}
	}
	if _, err := users.Create(ctx, User{Username: "bob", Role: RoleViewer}, "short", false, ""); !errors.Is(err, ErrInvalidUser) {
		t.Errorf("a short password must be refused: %v", err)
	}

	list, _ := users.List(ctx)
	if len(list) != 1 || list[0].Username != "anna" || list[0].Hash != "" || !list[0].MustChangePassword || list[0].CreatedBy != "local:admin" {
		t.Fatalf("list = %+v", list)
	}
	if _, err := users.Authenticate(ctx, "anna", "wrong password!"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("wrong password: %v", err)
	}
	if _, err := users.Authenticate(ctx, "nobody", "correct horse battery"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	u, err := users.Authenticate(ctx, " ANNA ", "correct horse battery")
	if err != nil || u.Role != RoleOperator {
		t.Fatalf("authenticate = %+v, %v", u, err)
	}

	// A session follows the account.
	id := u.Identity()
	id.IssuedAt = time.Now().Unix()
	admin := RoleAdmin
	if _, err := users.Update(ctx, "anna", UserPatch{Role: &admin}); err != nil {
		t.Fatal(err)
	}
	if cur, err := users.Session(ctx, &id); err != nil || cur.Role != RoleAdmin {
		t.Errorf("a role change applies to the session: %+v, %v", cur, err)
	}
	if err := users.SetPassword(ctx, "anna", "another long password", false); err != nil {
		t.Fatal(err)
	}
	list, _ = users.List(ctx)
	changed := list[0].PasswordChangedAt.Unix()
	if id.IssuedAt = changed - 1; func() error { _, err := users.Session(ctx, &id); return err }() == nil {
		t.Error("a password reset ends older sessions")
	}
	if id.IssuedAt = changed; func() error { _, err := users.Session(ctx, &id); return err }() != nil {
		t.Error("a session issued with the new password stays valid")
	}
	disabled := true
	if _, err := users.Update(ctx, "anna", UserPatch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Authenticate(ctx, "anna", "another long password"); !errors.Is(err, ErrUserDisabled) {
		t.Errorf("a disabled user cannot sign in: %v", err)
	}
	if err := users.Delete(ctx, "anna"); err != nil {
		t.Fatal(err)
	}
	if err := users.Delete(ctx, "anna"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestManagedUserSignInAndPasswordChange(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	if _, err := svc.Users.Create(ctx, User{Username: "anna", Role: RoleViewer}, "temporary password", true, ""); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/thing", Require(RoleViewer, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	mux.HandleFunc("GET /api/auth/me", svc.HandleMe)
	mux.HandleFunc("POST /api/auth/password", svc.HandleChangePassword)
	mux.HandleFunc("POST /api/login", svc.HandleLogin)
	h := svc.Middleware(mux)
	do := func(method, path string, body any, cookies []*http.Cookie) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.RemoteAddr = "10.0.0.2:1234"
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	rr := do(http.MethodPost, "/api/login", map[string]string{"username": "anna", "password": "temporary password"}, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("login = %d %s", rr.Code, rr.Body)
	}
	session := rr.Result().Cookies()
	if rr := do(http.MethodGet, "/api/thing", nil, session); rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "password_change_required") {
		t.Errorf("a user with a chosen-for-them password must change it first: %d %s", rr.Code, rr.Body)
	}
	if rr := do(http.MethodGet, "/api/auth/me", nil, session); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"mustChangePassword":true`) {
		t.Errorf("me = %d %s", rr.Code, rr.Body)
	}
	if rr := do(http.MethodPost, "/api/auth/password", map[string]string{"currentPassword": "wrong", "newPassword": "a brand new password"}, session); rr.Code != http.StatusForbidden {
		t.Errorf("wrong current password = %d", rr.Code)
	}
	rr = do(http.MethodPost, "/api/auth/password", map[string]string{"currentPassword": "temporary password", "newPassword": "a brand new password"}, session)
	if rr.Code != http.StatusOK {
		t.Fatalf("change = %d %s", rr.Code, rr.Body)
	}
	fresh := rr.Result().Cookies()
	if rr := do(http.MethodGet, "/api/thing", nil, fresh); rr.Code != http.StatusOK {
		t.Errorf("the re-issued session works: %d %s", rr.Code, rr.Body)
	}

	disabled := true
	if _, err := svc.Users.Update(ctx, "anna", UserPatch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if rr := do(http.MethodGet, "/api/thing", nil, fresh); rr.Code != http.StatusUnauthorized {
		t.Errorf("disabling ends the session: %d", rr.Code)
	}
	signIns, _ := svc.SignIns.List(ctx)
	if len(signIns) != 1 || signIns[0].Subject != "user:anna" || signIns[0].Count != 1 {
		t.Errorf("sign-ins = %+v", signIns)
	}
}
