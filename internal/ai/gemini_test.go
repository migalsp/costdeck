package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestGeminiListModels(t *testing.T) {
	var requests []*http.Request
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r)
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"API key not valid. Please pass a valid API key."}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{
			{"name": "models/gemini-2.5-pro", "supportedGenerationMethods": []string{"generateContent", "countTokens"}},
			{"name": "models/text-embedding-004", "supportedGenerationMethods": []string{"embedContent"}},
			{"name": "models/gemini-2.5-flash", "supportedGenerationMethods": []string{"generateContent"}},
		}})
	}))
	defer srv.Close()
	allowTestServers(t)
	p, err := NewProvider(&Settings{Provider: ProviderGemini, Model: "gemini-2.5-flash", BaseURL: srv.URL, APIKey: "g-key"})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := p.ListModels(context.Background())
	if err != nil || !reflect.DeepEqual(ids, []string{"gemini-2.5-flash", "gemini-2.5-pro"}) {
		t.Errorf("ListModels() = %v, %v; want generateContent models only, sorted, without the models/ prefix", ids, err)
	}
	if r := requests[0]; r.URL.Path != "/models" || r.URL.Query().Get("pageSize") != "1000" || r.Header.Get("x-goog-api-key") != "g-key" {
		t.Errorf("request = %s?%s with key %q", r.URL.Path, r.URL.RawQuery, r.Header.Get("x-goog-api-key"))
	}
	status = http.StatusForbidden
	if _, err := p.ListModels(context.Background()); err == nil || err.Error() != "Gemini rejected the API key: API key not valid. Please pass a valid API key." {
		t.Errorf("ListModels() error = %v", err)
	}
}

func TestGeminiToolCallsAndResults(t *testing.T) {
	rec := &recorder{ctype: "text/event-stream", replies: []string{
		sse(
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"weighing the options","thought":true}]}}]}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Let me check."}]}}]}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"abc","name":"get_group","args":{"name":"pps1"}}},{"functionCall":{"name":"get_group","args":null}},{"functionCall":{"name":"get_group","args":["pps1"]}}]},"finishReason":"STOP"}]}`,
		),
		sse(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"pps1 is up."}]},"finishReason":"STOP"}]}`),
	}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	sess := newTestSession(t, &Settings{Provider: ProviderGemini, Model: "gemini-2.5-flash", BaseURL: srv.URL, APIKey: "k"}, testTools[:1])

	events, emit := collect()
	calls, err := sess.Step(context.Background(), emit)
	if err != nil {
		t.Fatal(err)
	}
	if texts := eventsOfType(*events, "text"); len(texts) != 1 || texts[0].Text != "Let me check." {
		t.Errorf("text events = %+v; thoughts must not be shown", texts)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %+v", calls)
	}
	if c := calls[0]; c.ID != "abc" || c.Name != getGroup || c.RawArgs != `{"name":"pps1"}` || c.Args["name"] != "pps1" {
		t.Errorf("call 0 = %+v", c)
	}
	if c := calls[1]; c.ID != "fc_3" || c.Args == nil || len(c.Args) != 0 || c.Invalid != nil || c.RawArgs != "null" {
		t.Errorf("call 1 = %+v; a call without an id is named after its part and null arguments are an empty object", c)
	}
	if c := calls[2]; c.Invalid == nil || c.Args != nil || c.ID != "fc_4" {
		t.Errorf("call 2 = %+v; arguments that are not an object must be flagged", c)
	}
	if rec.pathSeen[0] != "/models/gemini-2.5-flash:streamGenerateContent" {
		t.Errorf("request path = %q", rec.pathSeen[0])
	}

	sess.AddResults([]ToolResult{
		{Call: calls[0], Content: "ScaledUp"},
		{Call: calls[1], Content: "no such group", IsError: true},
	})
	if _, err := sess.Step(context.Background(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	contents := rec.bodies[1]["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("second request contents = %v", contents)
	}
	model := contents[1].(map[string]any)
	if model["role"] != "model" || len(model["parts"].([]any)) != 5 {
		t.Errorf("model turn = %v; every part, thoughts included, is replayed", model)
	}
	responses := contents[2].(map[string]any)["parts"].([]any)
	ok := responses[0].(map[string]any)["functionResponse"].(map[string]any)
	failed := responses[1].(map[string]any)["functionResponse"].(map[string]any)
	if ok["id"] != "abc" || ok["response"].(map[string]any)["result"] != "ScaledUp" {
		t.Errorf("first response = %v; it must echo the call id and carry the result", ok)
	}
	if _, hasID := failed["id"]; hasID || failed["response"].(map[string]any)["error"] != "no such group" {
		t.Errorf("second response = %v; a synthetic fc_ id is not sent back and errors go under error", failed)
	}
	if contents[2].(map[string]any)["role"] != "user" {
		t.Errorf("function responses must come from the user role: %v", contents[2])
	}
}

func TestGeminiRefusalsAndLimits(t *testing.T) {
	tests := []struct {
		name  string
		chunk string
		check func(error) bool
	}{
		{"blocked prompt", `data: {"promptFeedback":{"blockReason":"SAFETY"}}`, func(err error) bool { return errors.Is(err, ErrRefused) }},
		{"safety stop", `data: {"candidates":[{"content":{"parts":[{"text":"I can"}]},"finishReason":"SAFETY"}]}`, func(err error) bool { return errors.Is(err, ErrRefused) }},
		{"prohibited content", `data: {"candidates":[{"content":{"parts":[]},"finishReason":"PROHIBITED_CONTENT"}]}`, func(err error) bool { return errors.Is(err, ErrRefused) }},
		{"blocklist", `data: {"candidates":[{"finishReason":"BLOCKLIST"}]}`, func(err error) bool { return errors.Is(err, ErrRefused) }},
		{"personal data", `data: {"candidates":[{"finishReason":"SPII"}]}`, func(err error) bool { return errors.Is(err, ErrRefused) }},
		{"output limit", `data: {"candidates":[{"content":{"parts":[{"text":"long"}]},"finishReason":"MAX_TOKENS"}]}`, func(err error) bool {
			return err != nil && strings.Contains(err.Error(), "output token limit")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{ctype: "text/event-stream", replies: []string{sse(tt.chunk)}}
			srv := httptest.NewServer(rec)
			defer srv.Close()
			sess := newTestSession(t, &Settings{Provider: ProviderGemini, Model: "m", BaseURL: srv.URL, APIKey: "k"}, nil)
			events, emit := collect()
			calls, err := sess.Step(context.Background(), emit)
			if calls != nil || !tt.check(err) {
				t.Errorf("Step() = %v, %v", calls, err)
			}
			if usage := eventsOfType(*events, "usage"); len(usage) != 1 || !usage[0].Usage.Estimated || usage[0].Usage.InputTokens == 0 {
				t.Errorf("usage events = %+v; without usageMetadata the tokens are estimated", usage)
			}
			if n := len(sess.(*geminiSession).contents); n != 1 {
				t.Errorf("contents = %d turns; a refused or cut-off answer must not be replayed", n)
			}
		})
	}
}

func TestGeminiRequestShape(t *testing.T) {
	rec := &recorder{ctype: "text/event-stream", replies: []string{sse(`data: {"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	allowTestServers(t)
	p, err := NewProvider(&Settings{Provider: ProviderGemini, Model: "m", BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	sess := p.NewSession("", []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}, {Role: "user", Content: "status?"}}, nil)
	if _, err := sess.Step(context.Background(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	body := rec.bodies[0]
	if _, ok := body["systemInstruction"]; ok {
		t.Errorf("no system prompt must mean no systemInstruction: %v", body)
	}
	if _, ok := body["tools"]; ok {
		t.Errorf("no tools must mean no tools list: %v", body)
	}
	contents := body["contents"].([]any)
	roles := make([]string, 0, len(contents))
	for _, c := range contents {
		roles = append(roles, c.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "user,model,user" {
		t.Errorf("roles = %v; assistant turns are model turns for Gemini", roles)
	}
}

func TestGeminiServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"The model is overloaded."}}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	sess := newTestSession(t, &Settings{Provider: ProviderGemini, Model: "m", BaseURL: srv.URL, APIKey: "k"}, nil)
	if _, err := sess.Step(context.Background(), func(Event) {}); err == nil || err.Error() != "Gemini returned HTTP 503: The model is overloaded." {
		t.Errorf("Step() error = %v", err)
	}
}

func TestGeminiSchema(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ids":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"mode":     map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "integer"}}},
			"nullable": map[string]any{"type": []any{"string", "null"}},
		},
		"required": []any{"ids"},
	}
	want := map[string]any{
		"type": "OBJECT",
		"properties": map[string]any{
			"ids":      map[string]any{"type": "ARRAY", "items": map[string]any{"type": "STRING"}},
			"mode":     map[string]any{"anyOf": []any{map[string]any{"type": "STRING"}, map[string]any{"type": "INTEGER"}}},
			"nullable": map[string]any{"type": []any{"string", "null"}},
		},
		"required": []any{"ids"},
	}
	if got := geminiSchema(in); !reflect.DeepEqual(got, want) {
		t.Errorf("geminiSchema() = %#v\nwant %#v", got, want)
	}
	if in["type"] != "object" {
		t.Error("geminiSchema() modified the tool's own schema")
	}
}
