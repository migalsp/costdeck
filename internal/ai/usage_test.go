package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// usageOf runs the agent once and sums the usage events.
func usageOf(t *testing.T, s *Settings) (Usage, int) {
	t.Helper()
	allowInternalHosts = true
	t.Cleanup(func() { allowInternalHosts = false })
	p, err := NewProvider(s)
	if err != nil {
		t.Fatal(err)
	}
	events, emit := collect()
	agent := &Agent{Provider: p, System: "You are a test.", Tools: testTools[:1]}
	if err := agent.Run(context.Background(), []Message{{Role: "user", Content: "status of pps1?"}}, emit); err != nil {
		t.Fatal(err)
	}
	var total Usage
	n := 0
	for _, e := range *events {
		if e.Type == "usage" {
			n++
			total.InputTokens += e.Usage.InputTokens
			total.OutputTokens += e.Usage.OutputTokens
			total.Estimated = total.Estimated || e.Usage.Estimated
		}
	}
	return total, n
}

func TestOpenAIReportsUsage(t *testing.T) {
	rec := &recorder{ctype: "text/event-stream", replies: []string{
		sse(`data: {"choices":[{"delta":{"content":"pps1 is up."},"finish_reason":"stop"}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":7}}`, `data: [DONE]`),
	}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	got, n := usageOf(t, &Settings{Provider: ProviderLocal, Model: "llama3", BaseURL: srv.URL + "/v1"})
	if n != 1 || got != (Usage{InputTokens: 120, OutputTokens: 7}) {
		t.Errorf("usage = %+v in %d events", got, n)
	}
	if opts, _ := rec.bodies[0]["stream_options"].(map[string]any); opts["include_usage"] != true {
		t.Errorf("the request does not ask for usage: %v", rec.bodies[0])
	}
}

// An endpoint that refuses stream_options is asked again without it, and its usage is
// estimated from the text.
func TestOpenAIEstimatesUsageWhenTheEndpointRefusesToReportIt(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		if strings.Contains(string(raw), "stream_options") {
			http.Error(w, `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse(`data: {"choices":[{"delta":{"content":"pps1 is up."},"finish_reason":"stop"}]}`, `data: [DONE]`))
	}))
	defer srv.Close()
	s := &Settings{Provider: ProviderLocal, Model: "old-server", BaseURL: srv.URL + "/v1"}
	got, n := usageOf(t, s)
	if n != 1 || !got.Estimated || got.OutputTokens != estimateTokens(len("pps1 is up.")) || got.InputTokens < 10 {
		t.Errorf("usage = %+v in %d events", got, n)
	}
	if len(bodies) != 2 {
		t.Fatalf("want a retry without stream_options, got %d requests", len(bodies))
	}
	// The refusal is remembered: the next request goes without it at once.
	usageOf(t, s)
	if len(bodies) != 3 || strings.Contains(bodies[2], "stream_options") {
		t.Errorf("requests = %d, last %s", len(bodies), bodies[len(bodies)-1])
	}
}

func TestAnthropicReportsUsageWithCachedTokens(t *testing.T) {
	start := func(id string, usage string) string {
		return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"" + id + "\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":" + usage + "}}"
	}
	rec := &recorder{ctype: "text/event-stream", replies: []string{sse(
		start("msg_1", `{"input_tokens":10,"cache_creation_input_tokens":200,"cache_read_input_tokens":1000,"output_tokens":1}`),
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"pps1 is up.\"}}",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":42}}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}",
	)}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	got, n := usageOf(t, &Settings{Provider: ProviderAnthropic, Model: "claude-opus-5-5", BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	if n != 1 || got != (Usage{InputTokens: 1210, OutputTokens: 42}) {
		t.Errorf("usage = %+v in %d events", got, n)
	}
}

func TestGeminiReportsUsage(t *testing.T) {
	rec := &recorder{ctype: "text/event-stream", replies: []string{sse(
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"pps1 "}]}}],"usageMetadata":{"promptTokenCount":80,"candidatesTokenCount":2}}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"is up."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":80,"candidatesTokenCount":5,"thoughtsTokenCount":30}}`,
	)}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	got, n := usageOf(t, &Settings{Provider: ProviderGemini, Model: "gemini-2.5-flash", BaseURL: srv.URL, APIKey: "k"})
	if n != 1 || got != (Usage{InputTokens: 80, OutputTokens: 35}) {
		t.Errorf("usage = %+v in %d events: the last totals count, thinking included", got, n)
	}
}
