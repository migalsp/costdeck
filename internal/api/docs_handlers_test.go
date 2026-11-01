package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/migalsp/costdeck-operator/internal/auth"
)

func TestAPIDocumentationIsPublic(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	t.Setenv("COSTDECK_AUTH_USER", "admin")
	t.Setenv("COSTDECK_AUTH_PASSWORD", "a-long-enough-password")
	server := buildMockServerWithK8s()
	svc, err := auth.NewService(context.Background(), server.Client)
	if err != nil {
		t.Fatal(err)
	}
	server.Auth = svc
	server.UI = testUI
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		return rr
	}

	spec := get("/api/openapi.yaml")
	if spec.Code != http.StatusOK || spec.Header().Get("Content-Type") != "application/x-yaml" || !bytes.Equal(spec.Body.Bytes(), openapiSpec) {
		t.Errorf("GET /api/openapi.yaml = %d %q, want the embedded spec without signing in", spec.Code, spec.Header().Get("Content-Type"))
	}
	page := get("/api/docs")
	body := page.Body.String()
	if page.Code != http.StatusOK || page.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
		!strings.Contains(body, "url: '/api/openapi.yaml'") || !strings.Contains(body, "swagger-ui-bundle.js") {
		t.Errorf("GET /api/docs = %d %q:\n%s", page.Code, page.Header().Get("Content-Type"), body)
	}
	if rr := get("/api/settings"); rr.Code != http.StatusUnauthorized {
		t.Errorf("fixture: GET /api/settings without a session = %d, want 401, or the documentation check proves nothing", rr.Code)
	}
}

func TestPagesLoadNothingTheyDoNotPin(t *testing.T) {
	t.Setenv("POD_NAMESPACE", operatorNamespace)
	server := buildMockServerWithK8s()
	server.UI = testUI
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		return rr
	}

	page := get("/api/docs")
	body := page.Body.String()
	external := regexp.MustCompile(`(?:src|href)="(https://[^"]+)"([^>]*)>`)
	matches := external.FindAllStringSubmatch(body, -1)
	if len(matches) != 2 {
		t.Fatalf("external resources on /api/docs = %d, want the stylesheet and the bundle", len(matches))
	}
	for _, m := range matches {
		if !strings.Contains(m[1], "swagger-ui-dist@5.33.3/") || !strings.Contains(m[2], `integrity="sha384-`) || !strings.Contains(m[2], `crossorigin="anonymous"`) {
			t.Errorf("%s is not pinned to an exact version with a Subresource Integrity hash: %s", m[1], m[0])
		}
	}
	sum := sha256.Sum256([]byte(swaggerInit))
	policy := page.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "script-src 'self' https://unpkg.com 'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") ||
		!strings.Contains(body, "<script>"+swaggerInit+"</script>") || strings.Contains(policy, "unsafe-eval") ||
		strings.Count(policy, "'unsafe-inline'") != 1 {
		t.Errorf("CSP of /api/docs = %q; it must allow unpkg and exactly the inline start-up script", policy)
	}

	for _, path := range []string{"/", "/api/version", "/settings"} {
		got := get(path).Header().Get("Content-Security-Policy")
		if !strings.Contains(got, "script-src 'self';") || !strings.Contains(got, "frame-ancestors 'none'") || strings.Contains(got, "unpkg") {
			t.Errorf("CSP of %s = %q, want scripts from this origin only", path, got)
		}
	}
}
