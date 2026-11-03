package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func allowTestServers(t *testing.T) {
	t.Helper()
	allowInternalHosts = true
	t.Cleanup(func() { allowInternalHosts = false })
}

func newTestSession(t *testing.T, s *Settings, tools []Tool) session {
	t.Helper()
	allowTestServers(t)
	p, err := NewProvider(s)
	if err != nil {
		t.Fatal(err)
	}
	return p.NewSession("You are a test.", []Message{{Role: "user", Content: "status of pps1?"}}, tools)
}

func eventsOfType(events []Event, typ string) []Event {
	var out []Event
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

const getGroup = "get_group"

func TestOpenAIStreamAssemblesInterleavedToolCalls(t *testing.T) {
	rec := &recorder{ctype: "text/event-stream", replies: []string{
		": keep-alive\n\n" + sse(
			`data: {"choices":[{"delta":{"content":"Checking "}}]}`,
			`data: {"choices":[{"delta":{"content":"both."}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"scale_group","arguments":""}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"get_group","arguments":"{\"name\":"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"name\":\"a\",\"action\":\"up\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"pps1\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":2,"id":"call_c","function":{"name":"get_group"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":3,"id":"call_d","function":{"name":"get_group","arguments":"[1,2]"}}]}}]}`,
			`data: a vendor extension that is not JSON`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":9}}`,
			`data: [DONE]`,
		),
		sse(`data: {"choices":[{"delta":{"content":"Done."},"finish_reason":"stop"}]}`, `data: [DONE]`),
	}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	sess := newTestSession(t, &Settings{Provider: ProviderLocal, Model: "llama3", BaseURL: srv.URL + "/v1", APIKey: "sk-1"}, testTools)

	events, emit := collect()
	calls, err := sess.Step(context.Background(), emit)
	if err != nil {
		t.Fatal(err)
	}
	texts := eventsOfType(*events, "text")
	if len(texts) != 2 || texts[0].Text != "Checking " || texts[1].Text != "both." {
		t.Errorf("text events = %+v, want each delta in order", texts)
	}
	if usage := eventsOfType(*events, "usage"); len(usage) != 1 || *usage[0].Usage != (Usage{InputTokens: 50, OutputTokens: 9}) {
		t.Errorf("usage events = %+v", usage)
	}
	checkInterleavedCalls(t, calls)
	checkFirstOpenAIRequest(t, rec)

	results := make([]ToolResult, len(calls))
	for i, c := range calls {
		results[i] = ToolResult{Call: c, Content: "result " + c.ID}
	}
	sess.AddResults(results)
	if _, err := sess.Step(context.Background(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	checkToolRoundReplayed(t, rec.bodies[1]["messages"].([]any))
}

func checkInterleavedCalls(t *testing.T, calls []ToolCall) {
	t.Helper()
	if len(calls) != 4 {
		t.Fatalf("calls = %+v, want four ordered by index", calls)
	}
	if c := calls[0]; c.ID != "call_0" || c.Name != getGroup || c.RawArgs != `{"name":"pps1"}` || !reflect.DeepEqual(c.Args, map[string]any{"name": "pps1"}) || c.Invalid != nil {
		t.Errorf("call 0 = %+v; fragments must join and a missing id becomes call_<index>", c)
	}
	if c := calls[1]; c.ID != "call_b" || c.Name != "scale_group" || c.Args["action"] != "up" {
		t.Errorf("call 1 = %+v", c)
	}
	if c := calls[2]; c.ID != "call_c" || c.RawArgs != "" || c.Args == nil || len(c.Args) != 0 || c.Invalid != nil {
		t.Errorf("call 2 = %+v; no arguments read as an empty object", c)
	}
	if c := calls[3]; c.Invalid == nil || c.Args != nil || c.RawArgs != "[1,2]" {
		t.Errorf("call 3 = %+v; arguments that are not an object must be flagged", c)
	}
}

func checkFirstOpenAIRequest(t *testing.T, rec *recorder) {
	t.Helper()
	first := rec.bodies[0]
	if first["model"] != "llama3" || first["stream"] != true || len(first["tools"].([]any)) != len(testTools) {
		t.Errorf("first request = %v", first)
	}
	if spec := first["tools"].([]any)[0].(map[string]any); spec["type"] != "function" || spec["function"].(map[string]any)["name"] != getGroup {
		t.Errorf("tool spec = %v", spec)
	}
	if rec.pathSeen[0] != "/v1/chat/completions" || rec.headers[0].Get("Authorization") != "Bearer sk-1" {
		t.Errorf("request went to %s with Authorization %q", rec.pathSeen[0], rec.headers[0].Get("Authorization"))
	}
}

func checkToolRoundReplayed(t *testing.T, msgs []any) {
	t.Helper()
	if len(msgs) != 7 {
		t.Fatalf("second request carries %d messages, want system, user, assistant and four tool results: %v", len(msgs), msgs)
	}
	assistant := msgs[2].(map[string]any)
	if assistant["role"] != roleAssistant || assistant["content"] != "Checking both." {
		t.Errorf("assistant turn = %v", assistant)
	}
	toolCalls := assistant["tool_calls"].([]any)
	ids := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		ids = append(ids, tc.(map[string]any)["id"].(string))
	}
	if strings.Join(ids, ",") != "call_0,call_b,call_c,call_d" {
		t.Errorf("assistant tool calls = %v, want them in index order", ids)
	}
	for i, id := range ids {
		tool := msgs[3+i].(map[string]any)
		if tool["role"] != "tool" || tool["tool_call_id"] != id || tool["content"] != "result "+id {
			t.Errorf("tool message %d = %v", i, tool)
		}
	}
}

func TestOpenAIFinishReasons(t *testing.T) {
	tests := []struct {
		finish string
		check  func(error) bool
	}{
		{"content_filter", func(err error) bool { return errors.Is(err, ErrRefused) }},
		{"length", func(err error) bool { return err != nil && strings.Contains(err.Error(), "output token limit") }},
	}
	for _, tt := range tests {
		t.Run(tt.finish, func(t *testing.T) {
			rec := &recorder{ctype: "text/event-stream", replies: []string{sse(
				`data: {"choices":[{"delta":{"content":"partial"}}]}`,
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"x","function":{"name":"get_group","arguments":"{\"na"}}]},"finish_reason":"`+tt.finish+`"}]}`,
				`data: [DONE]`,
			)}}
			srv := httptest.NewServer(rec)
			defer srv.Close()
			sess := newTestSession(t, &Settings{Provider: ProviderLocal, Model: "m", BaseURL: srv.URL}, testTools[:1])
			events, emit := collect()
			calls, err := sess.Step(context.Background(), emit)
			if calls != nil || !tt.check(err) {
				t.Errorf("Step() = %v, %v", calls, err)
			}
			if usage := eventsOfType(*events, "usage"); len(usage) != 1 || !usage[0].Usage.Estimated {
				t.Errorf("usage events = %+v; the tokens are spent even when the answer is cut off", usage)
			}
		})
	}
}

func TestOpenAIHTTPErrors(t *testing.T) {
	long := strings.Repeat("y", 400)
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"rejected key", http.StatusUnauthorized, `{"error":{"message":"Incorrect API key provided"}}`, "local rejected the API key: Incorrect API key provided"},
		{"forbidden", http.StatusForbidden, `plain text`, "local rejected the API key: plain text"},
		{"unknown model", http.StatusNotFound, `{"error":{"message":"The model gpt-9 does not exist"}}`, "local does not know the configured model or endpoint: The model gpt-9 does not exist"},
		{"rate limit", http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, "local rate limit reached; try again shortly"},
		{"bad request about something else", http.StatusBadRequest, `{"error":{"message":"messages must not be empty"}}`, "local returned HTTP 400: messages must not be empty"},
		{"long error page", http.StatusServiceUnavailable, long, "local returned HTTP 503: " + strings.Repeat("y", 300) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			sess := newTestSession(t, &Settings{Provider: ProviderLocal, Model: "m", BaseURL: srv.URL}, nil)
			_, err := sess.Step(context.Background(), func(Event) {})
			if err == nil || err.Error() != tt.want {
				t.Errorf("Step() error = %v\nwant %s", err, tt.want)
			}
			if requests != 1 {
				t.Errorf("requests = %d; only a refused stream_options is retried", requests)
			}
		})
	}
}

func TestOpenAIWithoutAKeySendsNoAuthorization(t *testing.T) {
	rec := &recorder{ctype: "text/event-stream", replies: []string{sse(`data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	sess := newTestSession(t, &Settings{Provider: ProviderLocal, Model: "m", BaseURL: srv.URL}, nil)
	if _, err := sess.Step(context.Background(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if auth, ok := rec.headers[0]["Authorization"]; ok {
		t.Errorf("Authorization = %v, want none for a keyless local server", auth)
	}
	if _, ok := rec.bodies[0]["tools"]; ok {
		t.Errorf("a session without tools must not send a tools list: %v", rec.bodies[0])
	}
}

func TestOpenAIUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	sess := newTestSession(t, &Settings{Provider: ProviderLocal, Model: "m", BaseURL: url}, nil)
	if _, err := sess.Step(context.Background(), func(Event) {}); err == nil || !strings.HasPrefix(err.Error(), "could not reach local: ") {
		t.Errorf("Step() error = %v", err)
	}
	p, _ := NewProvider(&Settings{Provider: ProviderLocal, Model: "m", BaseURL: url})
	if _, err := p.ListModels(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "could not reach local: ") {
		t.Errorf("ListModels() error = %v", err)
	}
}

func TestOpenAIListModels(t *testing.T) {
	var paths, auths []string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths, auths = append(paths, r.Method+" "+r.URL.Path), append(auths, r.Header.Get("Authorization"))
		if status != http.StatusOK {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "Invalid token"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "gpt-4o-mini"}, {"id": "gpt-4o"}, {"id": "dall-e-3"}}})
	}))
	defer srv.Close()
	allowTestServers(t)
	p, err := NewProvider(&Settings{Provider: ProviderOpenAI, Model: "gpt-4o", BaseURL: srv.URL + "/v1/", APIKey: "sk-2"})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := p.ListModels(context.Background())
	if err != nil || strings.Join(ids, ",") != "dall-e-3,gpt-4o,gpt-4o-mini" {
		t.Errorf("ListModels() = %v, %v; want the ids sorted", ids, err)
	}
	if paths[0] != "GET /v1/models" || auths[0] != "Bearer sk-2" {
		t.Errorf("request = %s with %q", paths[0], auths[0])
	}
	status = http.StatusUnauthorized
	if ids, err := p.ListModels(context.Background()); ids != nil || err == nil || err.Error() != "openai rejected the API key: Invalid token" {
		t.Errorf("ListModels() = %v, %v", ids, err)
	}
}
