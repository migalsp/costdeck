package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/auth"
)

// mcpServer builds an API server with authentication on and the MCP endpoint enabled,
// plus a viewer and an operator token.
func mcpTestServer(t *testing.T, enabled bool) (http.Handler, *Server, string, string) {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	t.Setenv("COSTDECK_AUTH_USER", "admin")
	t.Setenv("COSTDECK_AUTH_PASSWORD", "pw")
	server := buildMockServer()
	seedGroup(t, server, "pps1")
	if err := server.Client.Create(context.Background(), &finopsv1.CostDeckConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
		Spec: finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
			MCP: &finopsv1.MCPConfig{Enabled: enabled},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := auth.NewService(context.Background(), server.Client)
	if err != nil {
		t.Fatal(err)
	}
	server.Auth = svc
	viewer, _, err := svc.Tokens.Create(context.Background(), "viewer", auth.RoleViewer, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	operator, _, err := svc.Tokens.Create(context.Background(), "operator", auth.RoleOperator, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return handler, server, viewer, operator
}

func mcpCall(t *testing.T, h http.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

const (
	mcpListTools = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	mcpScaleDown = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"scale_group","arguments":{"name":"pps1","action":"down","duration":"2h"}}}`
)

func TestMCPToolsDependOnRole(t *testing.T) {
	h, server, viewer, operator := mcpTestServer(t, true)

	if rr := mcpCall(t, h, "", mcpListTools); rr.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous MCP call = %d, want 401", rr.Code)
	}

	rr := mcpCall(t, h, viewer, mcpListTools)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "list_scaling_groups") {
		t.Fatalf("viewer tools/list = %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"scale_group"`) {
		t.Error("a viewer token must not see action tools")
	}
	if rr := mcpCall(t, h, operator, mcpListTools); !strings.Contains(rr.Body.String(), `"scale_group"`) {
		t.Errorf("an operator token must see action tools: %s", rr.Body.String())
	}

	rr = mcpCall(t, h, viewer, mcpScaleDown)
	if !strings.Contains(rr.Body.String(), "operator role") {
		t.Errorf("a viewer calling an action must be refused: %s", rr.Body.String())
	}
	if g := fetchGroup(t, server, "pps1"); g.Spec.Active == nil || !*g.Spec.Active {
		t.Fatal("the refused call must not change the group")
	}

	rr = mcpCall(t, h, operator, mcpScaleDown)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `"isError":true`) {
		t.Fatalf("operator tools/call = %d %s", rr.Code, rr.Body.String())
	}
	if g := fetchGroup(t, server, "pps1"); g.Spec.Active == nil || *g.Spec.Active || g.Spec.ActiveUntil == nil {
		t.Errorf("operator action did not apply: active=%v until=%v", g.Spec.Active, g.Spec.ActiveUntil)
	}
}

func TestMCPDisabledAnswers404(t *testing.T) {
	h, _, viewer, _ := mcpTestServer(t, false)
	if rr := mcpCall(t, h, viewer, mcpListTools); rr.Code != http.StatusNotFound {
		t.Errorf("disabled MCP = %d, want 404", rr.Code)
	}
}
