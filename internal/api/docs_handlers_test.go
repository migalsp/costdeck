package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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
