package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRouterMethodAndFallbacks(t *testing.T) {
	server := buildMockServerWithK8s()
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

func TestAPIServerRunsOnEveryReplica(t *testing.T) {
	if (&Server{}).NeedLeaderElection() {
		t.Fatal("the API server must not wait for leader election, standby replicas would refuse traffic")
	}
}
