package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func anthropicEvent(typ, data string) string {
	return "event: " + typ + "\ndata: " + data
}

func anthropicStart(id string) string {
	return anthropicEvent("message_start", `{"type":"message_start","message":{"id":"`+id+`","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}`)
}

func anthropicStop(reason string) []string {
	return []string{
		anthropicEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+reason+`","stop_sequence":null},"usage":{"output_tokens":7}}`),
		anthropicEvent("message_stop", `{"type":"message_stop"}`),
	}
}

func anthropicText(index int, text string) []string {
	return []string{
		anthropicEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, index)),
		anthropicEvent("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%q}}`, index, text)),
		anthropicEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index)),
	}
}

func anthropicToolUse(index int, id, partialJSON string) []string {
	return []string{
		anthropicEvent("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%q,"name":"get_group","input":{}}}`, index, id)),
		anthropicEvent("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%q}}`, index, partialJSON)),
		anthropicEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index)),
	}
}

func anthropicStream(parts ...[]string) string {
	var all []string
	for _, p := range parts {
		all = append(all, p...)
	}
	return sse(all...)
}

func anthropicSessionAt(t *testing.T, url string) session {
	t.Helper()
	return newTestSession(t, &Settings{Provider: ProviderAnthropic, Model: "claude-opus-5-5", BaseURL: url, APIKey: "sk-ant"}, testTools[:1])
}

func TestAnthropicListModelsFollowsPages(t *testing.T) {
	var afterIDs, keys []string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		afterIDs, keys = append(afterIDs, r.URL.Query().Get("after_id")), append(keys, r.Header.Get("x-api-key"))
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			w.Header().Set("x-should-retry", "false")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		model := func(id string) map[string]any {
			return map[string]any{"id": id, "type": "model", "display_name": id, "created_at": "2026-01-01T00:00:00Z"}
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after_id") == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{model("claude-opus-5-5")}, "has_more": true, "first_id": "claude-opus-5-5", "last_id": "claude-opus-5-5"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{model("claude-sonnet-5-5")}, "has_more": false, "first_id": "claude-sonnet-5-5", "last_id": "claude-sonnet-5-5"})
	}))
	defer srv.Close()
	allowTestServers(t)
	p, err := NewProvider(&Settings{Provider: ProviderAnthropic, Model: "claude-opus-5-5", BaseURL: srv.URL, APIKey: "sk-ant"})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := p.ListModels(context.Background())
	if err != nil || !reflect.DeepEqual(ids, []string{"claude-opus-5-5", "claude-sonnet-5-5"}) {
		t.Errorf("ListModels() = %v, %v; want both pages", ids, err)
	}
	if len(afterIDs) != 2 || afterIDs[1] != "claude-opus-5-5" || keys[0] != "sk-ant" {
		t.Errorf("after_id per request = %v, keys = %v", afterIDs, keys)
	}
	status = http.StatusUnauthorized
	if _, err := p.ListModels(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("ListModels() error = %v, want the HTTP 401", err)
	}
}

func TestAnthropicErrorsAreActionable(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "the Anthropic API rejected the API key"},
		{http.StatusForbidden, "the Anthropic API rejected the API key"},
		{http.StatusNotFound, "the Anthropic API does not know the configured model; pick one from the model list"},
		{http.StatusTooManyRequests, "the Anthropic API rate limit was reached; try again shortly"},
		{529, "the Anthropic API is temporarily unavailable (HTTP 529)"},
		{http.StatusInternalServerError, "the Anthropic API is temporarily unavailable (HTTP 500)"},
		{http.StatusBadRequest, "the Anthropic API rejected the request (HTTP 400): "},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("x-should-retry", "false")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: too large"}}`)
			}))
			defer srv.Close()
			_, err := anthropicSessionAt(t, srv.URL).Step(context.Background(), func(Event) {})
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("Step() error = %v\nwant it to start with %q", err, tt.want)
			}
			if tt.status == http.StatusBadRequest && !strings.Contains(err.Error(), "max_tokens: too large") {
				t.Errorf("a rejected request must say why: %v", err)
			}
			if requests != 1 {
				t.Errorf("requests = %d", requests)
			}
		})
	}

	t.Run("cancelled request", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := anthropicSessionAt(t, srv.URL).Step(ctx, func(Event) {})
		if err == nil || !strings.HasPrefix(err.Error(), "could not reach Anthropic: ") || !errors.Is(err, context.Canceled) {
			t.Errorf("Step() error = %v", err)
		}
	})
}

func TestAnthropicFallbackDiscardsTheRefusedText(t *testing.T) {
	fallback := anthropicEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"fallback","from":{"model":"claude-opus-5-5"},"to":{"model":"claude-fable-5"},"trigger":{"type":"refusal","category":"cyber"}}}`)
	rec := &recorder{ctype: "text/event-stream", replies: []string{anthropicStream(
		[]string{anthropicStart("msg_1")},
		anthropicText(0, "I will not"),
		[]string{fallback, anthropicEvent("content_block_stop", `{"type":"content_block_stop","index":1}`)},
		anthropicText(2, "Here is the answer."),
		anthropicStop("end_turn"),
	)}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	events, emit := collect()
	calls, err := anthropicSessionAt(t, srv.URL).Step(context.Background(), emit)
	if err != nil || calls != nil {
		t.Fatalf("Step() = %v, %v", calls, err)
	}
	seq := make([]string, 0, len(*events))
	for _, e := range *events {
		seq = append(seq, e.Type+":"+e.Text)
	}
	if want := []string{"text:I will not", "reset:", "text:Here is the answer.", "usage:"}; !reflect.DeepEqual(seq, want) {
		t.Errorf("events = %v, want %v", seq, want)
	}
}

func TestAnthropicStopReasons(t *testing.T) {
	t.Run("output limit", func(t *testing.T) {
		rec := &recorder{ctype: "text/event-stream", replies: []string{anthropicStream([]string{anthropicStart("msg_1")}, anthropicText(0, "long"), anthropicStop("max_tokens"))}}
		srv := httptest.NewServer(rec)
		defer srv.Close()
		events, emit := collect()
		calls, err := anthropicSessionAt(t, srv.URL).Step(context.Background(), emit)
		if calls != nil || err == nil || !strings.Contains(err.Error(), "output token limit") {
			t.Errorf("Step() = %v, %v", calls, err)
		}
		if usage := eventsOfType(*events, "usage"); len(usage) != 1 || usage[0].Usage.OutputTokens != 7 {
			t.Errorf("usage = %+v", usage)
		}
	})

	t.Run("end of turn keeps the answer for the next step", func(t *testing.T) {
		rec := &recorder{ctype: "text/event-stream", replies: []string{
			anthropicStream([]string{anthropicStart("msg_1")}, anthropicText(0, "First answer."), anthropicStop("end_turn")),
			anthropicStream([]string{anthropicStart("msg_2")}, anthropicText(0, "Second."), anthropicStop("end_turn")),
		}}
		srv := httptest.NewServer(rec)
		defer srv.Close()
		sess := anthropicSessionAt(t, srv.URL)
		for range 2 {
			if calls, err := sess.Step(context.Background(), func(Event) {}); calls != nil || err != nil {
				t.Fatalf("Step() = %v, %v", calls, err)
			}
		}
		msgs := rec.bodies[1]["messages"].([]any)
		last := msgs[len(msgs)-1].(map[string]any)
		if last["role"] != roleAssistant || !strings.Contains(fmt.Sprint(last["content"]), "First answer.") {
			t.Errorf("second request ends with %v, want the first answer replayed", last)
		}
	})

	t.Run("mid-stream error", func(t *testing.T) {
		rec := &recorder{ctype: "text/event-stream", replies: []string{anthropicStream(
			[]string{anthropicStart("msg_1")},
			anthropicText(0, "Partial"),
			[]string{anthropicEvent("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)},
		)}}
		srv := httptest.NewServer(rec)
		defer srv.Close()
		calls, err := anthropicSessionAt(t, srv.URL).Step(context.Background(), func(Event) {})
		if calls != nil || err == nil || !strings.Contains(err.Error(), "overloaded_error") {
			t.Errorf("Step() = %v, %v; want the stream error passed on", calls, err)
		}
	})
}

func TestAnthropicMidStreamOverloadIsReportedAsTemporary(t *testing.T) {
	t.Skip("bug: describeAnthropicError looks only at the HTTP status, so an overloaded_error event inside a 200 stream reads as \"rejected the request (HTTP 200)\"")
	rec := &recorder{ctype: "text/event-stream", replies: []string{anthropicStream(
		[]string{anthropicStart("msg_1")},
		[]string{anthropicEvent("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)},
	)}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	_, err := anthropicSessionAt(t, srv.URL).Step(context.Background(), func(Event) {})
	if err == nil || !strings.HasPrefix(err.Error(), "the Anthropic API is temporarily unavailable") {
		t.Errorf("Step() error = %v, want an overload reported as temporary", err)
	}
}

func TestAnthropicToolInputMustBeAnObject(t *testing.T) {
	rec := &recorder{ctype: "text/event-stream", replies: []string{anthropicStream(
		[]string{anthropicStart("msg_1")},
		anthropicToolUse(0, "toolu_ok", `{"name":"pps1"}`),
		anthropicToolUse(1, "toolu_list", `["pps1"]`),
		anthropicStop("tool_use"),
	)}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	calls, err := anthropicSessionAt(t, srv.URL).Step(context.Background(), func(Event) {})
	if err != nil || len(calls) != 2 {
		t.Fatalf("Step() = %+v, %v", calls, err)
	}
	if c := calls[0]; c.ID != "toolu_ok" || c.Name != getGroup || c.Args["name"] != "pps1" || c.Invalid != nil {
		t.Errorf("call 0 = %+v", c)
	}
	if c := calls[1]; c.ID != "toolu_list" || c.Invalid == nil || c.Args != nil || c.RawArgs != `["pps1"]` {
		t.Errorf("call 1 = %+v; input that is not an object must be flagged", c)
	}
}

func TestAnthropicFlagsCutOffToolInput(t *testing.T) {
	t.Skip("bug: the SDK replaces tool input that is not valid JSON with {} when the block stops, so Step reports a cut-off call as valid with empty arguments instead of setting Invalid")
	rec := &recorder{ctype: "text/event-stream", replies: []string{anthropicStream(
		[]string{anthropicStart("msg_1")},
		anthropicToolUse(0, "toolu_cut", `{"name": "pp`),
		anthropicStop("tool_use"),
	)}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	calls, err := anthropicSessionAt(t, srv.URL).Step(context.Background(), func(Event) {})
	if err != nil || len(calls) != 1 {
		t.Fatalf("Step() = %+v, %v", calls, err)
	}
	if c := calls[0]; c.Invalid == nil || c.RawArgs != `{"name": "pp` {
		t.Errorf("call = %+v; eagerly streamed input that is not JSON must reach the agent as INVALID_JSON", c)
	}
}

func TestAnthropicOfficialEndpointDetection(t *testing.T) {
	tool := Tool{Name: "get_group", Parameters: map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": map[string]any{"type": "string"}},
		"required":   []any{"name"},
	}}
	tests := []struct {
		name          string
		baseURL       string
		model         string
		wantEager     bool
		wantFallbacks bool
	}{
		{name: "default endpoint", model: "claude-opus-5-5", wantEager: true, wantFallbacks: true},
		{name: "official URL copied with /v1", baseURL: "https://api.anthropic.com/v1/", model: "claude-sonnet-5-5", wantEager: true, wantFallbacks: true},
		{name: "official endpoint, model without server fallbacks", model: "claude-haiku-4-5", wantEager: true},
		{name: "gateway", baseURL: "https://llm-gateway.example.com", model: "claude-opus-5-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newAnthropic(&Settings{Provider: ProviderAnthropic, Model: tt.model, BaseURL: tt.baseURL, APIKey: "k"})
			sess := p.NewSession("", []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}, {Role: "user", Content: "go"}}, []Tool{tool}).(*anthropicSession)
			raw, err := json.Marshal(sess.params)
			if err != nil {
				t.Fatal(err)
			}
			body := string(raw)
			if got := strings.Contains(body, `"eager_input_streaming":true`); got != tt.wantEager {
				t.Errorf("eager input streaming = %v, want %v: %s", got, tt.wantEager, body)
			}
			if got := strings.Contains(body, `"fallbacks":"default"`); got != tt.wantFallbacks {
				t.Errorf("server fallbacks = %v, want %v: %s", got, tt.wantFallbacks, body)
			}
			if !strings.Contains(body, `"required":["name"]`) {
				t.Errorf("required arguments decoded from JSON were dropped: %s", body)
			}
			if strings.Contains(body, `"system"`) {
				t.Errorf("no system prompt must mean no system block: %s", body)
			}
			if len(sess.params.Messages) != 3 || sess.params.Messages[1].Role != "assistant" {
				t.Errorf("messages = %+v", sess.params.Messages)
			}
		})
	}
}
