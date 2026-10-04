package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// geminiProvider speaks the Gemini API (generativelanguage.googleapis.com).
type geminiProvider struct {
	s *Settings
}

type geminiContent struct {
	Role  string           `json:"role"`
	Parts []map[string]any `json:"parts"`
}

func (p *geminiProvider) NewSession(system string, history []Message, tools []Tool) session {
	turns := sanitizeHistory(history)
	contents := make([]geminiContent, 0, len(turns))
	for _, m := range turns {
		role := "user"
		if m.Role == roleAssistant {
			role = "model"
		}
		contents = append(contents, geminiContent{Role: role, Parts: []map[string]any{{"text": m.Content}}})
	}
	decls := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		decls = append(decls, map[string]any{"name": t.Name, "description": t.Description, "parameters": geminiSchema(t.Parameters)})
	}
	return &geminiSession{p: p, system: system, contents: contents, decls: decls}
}

// geminiSchema converts a JSON Schema to the OpenAPI subset Gemini expects (upper-case
// type names), recursively.
func geminiSchema(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if k == "type" {
				if s, ok := val.(string); ok {
					out[k] = strings.ToUpper(s)
					continue
				}
			}
			out[k] = geminiSchema(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = geminiSchema(t[i])
		}
		return out
	default:
		return v
	}
}

func (p *geminiProvider) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.s.baseURL()+"/models?pageSize=1000", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", p.s.APIKey)
	resp, err := p.s.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach Gemini: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, providerHTTPError("Gemini", resp)
	}
	var out struct {
		Models []struct {
			Name    string   `json:"name"`
			Methods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range out.Models {
		if slices.Contains(m.Methods, "generateContent") {
			ids = append(ids, strings.TrimPrefix(m.Name, "models/"))
		}
	}
	sort.Strings(ids)
	return ids, nil
}

type geminiSession struct {
	p        *geminiProvider
	system   string
	contents []geminiContent
	decls    []map[string]any
}

type geminiChunk struct {
	Candidates []struct {
		Content struct {
			Parts []map[string]any `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
}

func (s *geminiSession) Step(ctx context.Context, emit func(Event)) ([]ToolCall, error) {
	body := map[string]any{"contents": s.contents}
	if s.system != "" {
		body["systemInstruction"] = map[string]any{"parts": []map[string]any{{"text": s.system}}}
	}
	if len(s.decls) > 0 {
		body["tools"] = []map[string]any{{"functionDeclarations": s.decls}}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse", s.p.s.baseURL(), url.PathEscape(s.p.s.Model))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", s.p.s.APIKey)

	resp, err := s.p.s.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach Gemini: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, providerHTTPError("Gemini", resp)
	}

	// Every part is replayed exactly as received: function calls carry thought
	// signatures that Gemini requires back verbatim.
	var parts []map[string]any
	finish, blocked := "", ""
	err = readSSE(resp.Body, func(data string) error {
		var chunk geminiChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil
		}
		if chunk.PromptFeedback.BlockReason != "" {
			blocked = chunk.PromptFeedback.BlockReason
		}
		for _, cand := range chunk.Candidates {
			for _, part := range cand.Content.Parts {
				parts = append(parts, part)
				if thought, _ := part["thought"].(bool); thought {
					continue
				}
				if text, ok := part["text"].(string); ok && text != "" {
					emit(Event{Type: "text", Text: text})
				}
			}
			if cand.FinishReason != "" {
				finish = cand.FinishReason
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	switch {
	case blocked != "", finish == "SAFETY", finish == "PROHIBITED_CONTENT", finish == "BLOCKLIST", finish == "SPII":
		return nil, ErrRefused
	case finish == "MAX_TOKENS":
		return nil, fmt.Errorf("the answer hit the output token limit")
	}
	if len(parts) > 0 {
		s.contents = append(s.contents, geminiContent{Role: "model", Parts: parts})
	}

	var calls []ToolCall
	for i, part := range parts {
		fc, ok := part["functionCall"].(map[string]any)
		if !ok {
			continue
		}
		call := ToolCall{Name: fmt.Sprint(fc["name"])}
		if id, ok := fc["id"].(string); ok && id != "" {
			call.ID = id
		} else {
			call.ID = fmt.Sprintf("fc_%d", i)
		}
		raw, _ := json.Marshal(fc["args"])
		call.RawArgs = string(raw)
		switch args := fc["args"].(type) {
		case map[string]any:
			call.Args = args
		case nil:
			call.Args = map[string]any{}
		default:
			call.Invalid = fmt.Errorf("arguments are not a JSON object")
		}
		calls = append(calls, call)
	}
	return calls, nil
}

func (s *geminiSession) AddResults(results []ToolResult) {
	parts := make([]map[string]any, 0, len(results))
	for _, r := range results {
		key := "result"
		if r.IsError {
			key = "error"
		}
		fr := map[string]any{"name": r.Call.Name, "response": map[string]any{key: r.Content}}
		if !strings.HasPrefix(r.Call.ID, "fc_") {
			fr["id"] = r.Call.ID
		}
		parts = append(parts, map[string]any{"functionResponse": fr})
	}
	s.contents = append(s.contents, geminiContent{Role: "user", Parts: parts})
}
