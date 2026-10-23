package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// openAIProvider speaks the OpenAI Chat Completions API, which OpenAI, most gateways and
// self-hosted servers (Ollama, vLLM, LM Studio) implement.
type openAIProvider struct {
	s *Settings
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (p *openAIProvider) NewSession(system string, history []Message, tools []Tool) session {
	msgs := []oaMessage{}
	if system != "" {
		msgs = append(msgs, oaMessage{Role: "system", Content: new(system)})
	}
	for _, m := range sanitizeHistory(history) {
		msgs = append(msgs, oaMessage{Role: m.Role, Content: new(m.Content)})
	}
	specs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		specs = append(specs, map[string]any{
			"type":     "function",
			"function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Parameters},
		})
	}
	return &openAISession{p: p, messages: msgs, tools: specs}
}

func (p *openAIProvider) ListModels(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := p.getJSON(ctx, p.s.baseURL()+"/models", &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

func (p *openAIProvider) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	p.authorize(req)
	resp, err := p.s.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", p.s.Provider, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return providerHTTPError(p.s.Provider, resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (p *openAIProvider) authorize(req *http.Request) {
	if p.s.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.s.APIKey)
	}
}

type openAISession struct {
	p        *openAIProvider
	messages []oaMessage
	tools    []map[string]any
}

type oaChunk struct {
	// Usage comes in the last chunk when stream_options.include_usage is set.
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// noStreamUsage remembers the endpoints that refused stream_options: their usage is
// estimated instead.
var noStreamUsage sync.Map

// post sends a chat completion request, asking for the token usage unless the endpoint
// refused that before. An endpoint that refuses it is asked once more without.
func (s *openAISession) post(ctx context.Context, body map[string]any) (*http.Response, []byte, error) {
	endpoint := s.p.s.baseURL() + "/chat/completions"
	_, refused := noStreamUsage.Load(endpoint)
	if !refused {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	s.p.authorize(req)
	resp, err := s.p.s.httpClient().Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("could not reach %s: %w", s.p.s.Provider, err)
	}
	if resp.StatusCode == http.StatusBadRequest && !refused {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		if strings.Contains(string(msg), "stream_options") {
			noStreamUsage.Store(endpoint, true)
			delete(body, "stream_options")
			return s.post(ctx, body)
		}
		resp.Body = io.NopCloser(bytes.NewReader(msg))
	}
	return resp, payload, nil
}

func (s *openAISession) Step(ctx context.Context, emit func(Event)) ([]ToolCall, error) {
	body := map[string]any{"model": s.p.s.Model, "stream": true, "messages": s.messages}
	if len(s.tools) > 0 {
		body["tools"] = s.tools
	}
	resp, payload, err := s.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, providerHTTPError(s.p.s.Provider, resp)
	}

	var text strings.Builder
	calls := map[int]*oaToolCall{}
	finish := ""
	var usage *Usage
	err = readSSE(resp.Body, func(data string) error {
		var chunk oaChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil // Keep-alive comments and vendor extensions are skipped.
		}
		if u := chunk.Usage; u != nil && u.PromptTokens > 0 {
			usage = &Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				emit(Event{Type: "text", Text: choice.Delta.Content})
			}
			// Tool call fragments are keyed by index; id and name arrive only once.
			for _, tc := range choice.Delta.ToolCalls {
				c, ok := calls[tc.Index]
				if !ok {
					c = &oaToolCall{Type: "function"}
					calls[tc.Index] = c
				}
				if tc.ID != "" {
					c.ID = tc.ID
				}
				if tc.Function.Name != "" {
					c.Function.Name = tc.Function.Name
				}
				c.Function.Arguments += tc.Function.Arguments
			}
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if usage == nil {
		out := text.Len()
		for _, c := range calls {
			out += len(c.Function.Name) + len(c.Function.Arguments)
		}
		usage = &Usage{InputTokens: estimateTokens(len(payload)), OutputTokens: estimateTokens(out), Estimated: true}
	}
	emit(Event{Type: "usage", Usage: usage})
	if finish == "content_filter" {
		return nil, ErrRefused
	}
	if finish == "length" {
		return nil, fmt.Errorf("the answer hit the output token limit")
	}

	indexes := make([]int, 0, len(calls))
	for i := range calls {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)

	assistant := oaMessage{Role: "assistant"}
	if text.Len() > 0 {
		assistant.Content = new(text.String())
	}
	var out []ToolCall
	for _, i := range indexes {
		c := calls[i]
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i)
		}
		assistant.ToolCalls = append(assistant.ToolCalls, *c)
		call := ToolCall{ID: c.ID, Name: c.Function.Name, RawArgs: c.Function.Arguments}
		raw := c.Function.Arguments
		if strings.TrimSpace(raw) == "" {
			raw = "{}"
		}
		if err := json.Unmarshal([]byte(raw), &call.Args); err != nil || call.Args == nil {
			call.Invalid, call.Args = fmt.Errorf("arguments are not a JSON object"), nil
		}
		out = append(out, call)
	}
	s.messages = append(s.messages, assistant)
	return out, nil
}

func (s *openAISession) AddResults(results []ToolResult) {
	for _, r := range results {
		s.messages = append(s.messages, oaMessage{Role: "tool", ToolCallID: r.Call.ID, Content: new(r.Content)})
	}
}

// readSSE calls fn with the payload of every "data:" line until the stream ends.
func readSSE(body io.Reader, fn func(data string) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		if err := fn(data); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read model stream: %w", err)
	}
	return nil
}

// providerHTTPError reads an error response into an actionable message.
func providerHTTPError(provider string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	msg := strings.TrimSpace(string(body))
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		msg = parsed.Error.Message
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s rejected the API key: %s", provider, msg)
	case http.StatusNotFound:
		return fmt.Errorf("%s does not know the configured model or endpoint: %s", provider, msg)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%s rate limit reached; try again shortly", provider)
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return fmt.Errorf("%s returned HTTP %d: %s", provider, resp.StatusCode, msg)
}
