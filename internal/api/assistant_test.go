package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

func TestToolsForViewerHideMutatingActions(t *testing.T) {
	server := buildMockServer()
	for _, tool := range server.toolsFor(false) {
		if tool.Mutating {
			t.Errorf("viewer was offered mutating tool %s", tool.Name)
		}
	}
	var mutating int
	for _, tool := range server.toolsFor(true) {
		if tool.Mutating {
			mutating++
			if tool.Run != nil {
				t.Errorf("mutating tool %s must not be runnable by the model", tool.Name)
			}
		}
	}
	if mutating == 0 {
		t.Error("operators must be offered the mutating tools")
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
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
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
