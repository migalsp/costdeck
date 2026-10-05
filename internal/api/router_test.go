package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

var testUI = fstest.MapFS{"index.html": {Data: []byte(`<!doctype html><div id="root"></div>`)}}

func TestRouterMethodAndFallbacks(t *testing.T) {
	server := buildMockServerWithK8s()
	server.UI = testUI
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		method     string
		target     string
		wantStatus int
		wantBody   string
	}{
		{"wrong method is rejected by the mux", http.MethodDelete, "/api/scaling/groups", http.StatusMethodNotAllowed, ""},
		{"unknown API path is a 404, not the dashboard", http.MethodGet, "/api/does-not-exist", http.StatusNotFound, "404"},
		{"client-side route falls back to index.html", http.MethodGet, "/auth/callback", http.StatusOK, "<div id=\"root\">"},
		{"root serves the dashboard", http.MethodGet, "/", http.StatusOK, "<div id=\"root\">"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(tt.method, tt.target, nil))
			if rr.Code != tt.wantStatus {
				t.Fatalf("%s %s = %d, want %d", tt.method, tt.target, rr.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && !strings.Contains(rr.Body.String(), tt.wantBody) {
				t.Errorf("%s %s body does not contain %q:\n%s", tt.method, tt.target, tt.wantBody, rr.Body.String())
			}
			if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

func TestBuildWithoutDashboardExplainsItself(t *testing.T) {
	server := buildMockServerWithK8s()
	server.UI = fstest.MapFS{}
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "make ui") {
		t.Errorf("GET / = %d %q, want 503 pointing at `make ui`", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("GET /api/version = %d, the API must keep working without the dashboard", rr.Code)
	}
}

func TestAPIServerRunsOnEveryReplica(t *testing.T) {
	if (&Server{}).NeedLeaderElection() {
		t.Fatal("the API server must not wait for leader election, standby replicas would refuse traffic")
	}
}
