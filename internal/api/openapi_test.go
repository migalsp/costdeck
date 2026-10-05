package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/migalsp/costdeck-operator/internal/auth"
)

// routePattern matches the registrations in routes(): the wrapper names the minimum role.
// A bare mux.HandleFunc is public only if the auth middleware lets the path through;
// otherwise any signed-in user (viewer or above) may call it.
var routePattern = regexp.MustCompile(`(viewer|operator|admin|mux\.HandleFunc)\("(GET|POST|PUT|DELETE|PATCH) (/api/[^"]+)"`)

// rolePublic marks endpoints that need no session.
const rolePublic = "public"

var httpMethods = map[string]bool{"get": true, "post": true, "put": true, "delete": true, "patch": true}

type specOperation struct {
	Role     string             `json:"x-role"`
	Security []map[string][]any `json:"security"`
}

// TestOpenAPIMatchesRoutes keeps the published API reference honest: every route is
// documented with the role it requires, and nothing is documented that is not served.
// The dashboard's API reference is rendered from the same spec.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := map[string]string{} // "get /api/x" -> role or rolePublic
	for _, m := range routePattern.FindAllStringSubmatch(string(src), -1) {
		role := m[1]
		if strings.HasPrefix(role, "mux") {
			role = "viewer"
			if auth.PublicPath(m[3]) {
				role = rolePublic
			}
		}
		routes[strings.ToLower(m[2])+" "+m[3]] = role
	}
	if len(routes) < 40 {
		t.Fatalf("found only %d routes in server.go; has the registration style changed?", len(routes))
	}

	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := yaml.Unmarshal(openapiSpec, &spec); err != nil {
		t.Fatalf("openapi.yaml: %v", err)
	}
	documented := map[string]bool{}
	for path, items := range spec.Paths {
		for method, raw := range items {
			if !httpMethods[method] {
				continue // path-level parameters, summary, …
			}
			var op specOperation
			if err := json.Unmarshal(raw, &op); err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			key := method + " " + path
			documented[key] = true
			role, ok := routes[key]
			switch {
			case !ok:
				t.Errorf("openapi.yaml documents %s, which the server does not serve", key)
			case role == rolePublic && (op.Security == nil || len(op.Security) != 0):
				t.Errorf("%s is public; document it with security: []", key)
			case role != rolePublic && op.Role != role:
				t.Errorf("%s requires the %s role; openapi.yaml says x-role: %q", key, role, op.Role)
			}
		}
	}
	for key := range routes {
		if !documented[key] {
			t.Errorf("%s is served but missing from openapi.yaml", key)
		}
	}
}

func TestOpenAPIJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	handleOpenAPIJSON(rr, httptest.NewRequest(http.MethodGet, "/api/openapi.json", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var spec struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &spec); err != nil || len(spec.Paths) == 0 {
		t.Fatalf("not a usable spec: %v", err)
	}
}
