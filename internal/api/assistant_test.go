package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

func as(role auth.Role) context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{Subject: "user:" + string(role), Role: role})
}

func TestToolsForViewerHideMutatingActions(t *testing.T) {
	server := buildMockServer()
	for _, tool := range server.toolsFor(as(auth.RoleViewer)) {
		if tool.Mutating {
			t.Errorf("viewer was offered mutating tool %s", tool.Name)
		}
	}
	var mutating int
	for _, tool := range server.toolsFor(as(auth.RoleOperator)) {
		if tool.Mutating {
			mutating++
			if tool.Run != nil {
				t.Errorf("mutating tool %s must not be runnable by the model", tool.Name)
			}
		}
		if tool.AdminOnly {
			t.Errorf("operator was offered admin tool %s", tool.Name)
		}
	}
	if mutating == 0 {
		t.Error("operators must be offered the mutating tools")
	}
	var admin int
	for _, tool := range server.toolsFor(as(auth.RoleAdmin)) {
		if tool.AdminOnly {
			admin++
		}
	}
	if admin == 0 {
		t.Error("admins must be offered the tools that change schedules")
	}
}

func execTool(t *testing.T, server *Server, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/ai/tools/"+name, bytes.NewBufferString(body)))
	return rr
}

func TestConfirmedScaleActionHoldsUntilNextTransition(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	seedGroup(t, server, "pps1")

	before := time.Now()
	rr := execTool(t, server, "scale_group", `{"args":{"name":"pps1","action":"down"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("execute = %d: %s", rr.Code, rr.Body.String())
	}
	g := fetchGroup(t, server, "pps1")
	if g.Spec.Active == nil || *g.Spec.Active {
		t.Fatalf("spec.active = %v, want false", g.Spec.Active)
	}
	want := (&scaling.Engine{}).NextScheduleChange(before, g.Spec.Schedules)
	if g.Spec.ActiveUntil == nil || want == nil || !g.Spec.ActiveUntil.Time.Equal(*want) {
		t.Errorf("activeUntil = %v, want the next schedule change %v", g.Spec.ActiveUntil, want)
	}

	if rr := execTool(t, server, "scale_group", `{"args":{"name":"pps1","action":"resume"}}`); rr.Code != http.StatusOK {
		t.Fatalf("resume = %d", rr.Code)
	}
	if g := fetchGroup(t, server, "pps1"); g.Spec.Active != nil || g.Spec.ActiveUntil != nil {
		t.Errorf("resume must clear the override, got active=%v until=%v", g.Spec.Active, g.Spec.ActiveUntil)
	}
}

func TestConfirmedActionsAreValidated(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	seedGroup(t, server, "pps1")

	for name, body := range map[string]string{
		"scale_group":          `{"args":{"name":"pps1","action":"explode"}}`,
		"get_cluster_overview": `{"args":{}}`, // read-only tools are not "actions"
		"drop_database":        `{"args":{}}`,
	} {
		if rr := execTool(t, server, name, body); rr.Code < 400 {
			t.Errorf("%s %s = %d, want a client error", name, body, rr.Code)
		}
	}
	if rr := execTool(t, server, "scale_group", `{"args":{"name":"missing","action":"up"}}`); rr.Code != http.StatusNotFound {
		t.Errorf("scaling a missing group = %d, want 404", rr.Code)
	}
}

func TestAIChatStreamsToolActivityAndAnswer(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		calls++
		if calls == 1 {
			_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"list_scaling_groups","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`+"\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"You have one group."}}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer model.Close()

	server := buildMockServer()
	seedGroup(t, server, "pps1")
	if err := server.Client.Create(context.Background(), &finopsv1.CostDeckConfig{
		Name: "default", Namespace: "costdeck",
		Spec: finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
			AI: &finopsv1.AIIntegrationConfig{Enabled: true, Provider: "local", Model: "llama3", BaseURL: model.URL + "/v1"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/ai/chat",
		bytes.NewBufferString(`{"messages":[{"role":"user","content":"how many groups?"}]}`)))
	body := rr.Body.String()
	if rr.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q: %s", rr.Header().Get("Content-Type"), body)
	}
	for _, want := range []string{`"type":"tool_call","tool":"list_scaling_groups"`, `"type":"tool_result"`, `"text":"You have one group."`, `"type":"done"`} {
		if !strings.Contains(body, want) {
			t.Errorf("stream is missing %s:\n%s", want, body)
		}
	}
}

func TestAIChatRequiresEnabledIntegration(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/ai/chat",
		bytes.NewBufferString(`{"messages":[{"role":"user","content":"hi"}]}`)))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "disabled") {
		t.Errorf("chat with AI disabled = %d %s", rr.Code, rr.Body.String())
	}
}

// TestToolSchemasAreStrictJSONSchema keeps every tool's parameters acceptable to strict
// validators (vLLM, some MCP clients): "required" is absent or a non-empty list of declared
// properties, never null.
func TestToolSchemasAreStrictJSONSchema(t *testing.T) {
	server := buildMockServerWithK8s()
	for _, tool := range server.toolRegistry() {
		raw, err := json.Marshal(tool.Parameters)
		if err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		var schema map[string]any
		_ = json.Unmarshal(raw, &schema)
		props, ok := schema["properties"].(map[string]any)
		if schema["type"] != "object" || !ok {
			t.Errorf("%s: parameters must be an object schema with properties: %s", tool.Name, raw)
			continue
		}
		req, present := schema["required"]
		if !present {
			continue
		}
		list, ok := req.([]any)
		if !ok || len(list) == 0 {
			t.Errorf("%s: required must be omitted or a non-empty array, got %s", tool.Name, raw)
			continue
		}
		for _, name := range list {
			if _, declared := props[name.(string)]; !declared {
				t.Errorf("%s: required %q is not a declared property", tool.Name, name)
			}
		}
	}
}

// The assistant sees why a group sleeps for being unused, and until when.
func TestGetScalingGroupToolShowsIdleSleep(t *testing.T) {
	server := buildMockServerWithK8s()
	until := metav1.NewTime(time.Date(2026, 10, 23, 18, 1, 0, 0, time.UTC))
	g := &finopsv1.ScalingGroup{Name: "shop", Namespace: config.OperatorNamespace(),
		Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"shop-dev"}, SleepPage: true, IdleAfter: &metav1.Duration{Duration: time.Hour}}}
	g.Status.Mode, g.Status.IdleUntil = "Idle", &until
	if err := server.Client.Create(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	out, err := server.toolGetGroup(context.Background(), "shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"idleAfter":"1h0m0s"`, `"sleepPage":true`, `"idleUntil":"2026-10-23T18:01:00Z"`, `"mode":"Idle"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if !strings.Contains(assistantSystemPrompt, "Idle (") {
		t.Error("the system prompt does not explain the Idle mode")
	}
}
