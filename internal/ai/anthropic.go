package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// anthropicMaxTokens leaves the model room for long answers; requests are streamed, so a
// high ceiling costs nothing unless it is used.
const anthropicMaxTokens = 64000

// serverFallbackModels accept `fallbacks: "default"`: when a safety classifier declines a
// request, the API re-runs it on the model Anthropic recommends for that category instead
// of returning the refusal.
var serverFallbackModels = []string{"claude-opus-5-5", "claude-fable-5-1", "claude-fable-5", "claude-opus-5", "claude-sonnet-5-5"}

type anthropicProvider struct {
	s      *Settings
	client anthropic.Client
	// official is true when requests go to the Anthropic API itself rather than a proxy
	// or gateway that may not understand newer request fields.
	official bool
}

func newAnthropic(s *Settings) *anthropicProvider {
	opts := []option.RequestOption{option.WithAPIKey(s.APIKey), option.WithHTTPClient(s.httpClient())}
	official := true
	if s.BaseURL != "" {
		// The SDK appends /v1/messages itself; tolerate a base URL copied with /v1.
		base := strings.TrimSuffix(strings.TrimRight(s.BaseURL, "/"), "/v1")
		opts = append(opts, option.WithBaseURL(base))
		official = strings.EqualFold(strings.TrimRight(base, "/"), "https://api.anthropic.com")
	}
	return &anthropicProvider{s: s, client: anthropic.NewClient(opts...), official: official}
}

func (p *anthropicProvider) NewSession(system string, history []Message, tools []Tool) session {
	msgs := make([]anthropic.BetaMessageParam, 0, len(history))
	for _, m := range sanitizeHistory(history) {
		block := anthropic.NewBetaTextBlock(m.Content)
		if m.Role == roleAssistant {
			msgs = append(msgs, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: []anthropic.BetaContentBlockParamUnion{block}})
		} else {
			msgs = append(msgs, anthropic.NewBetaUserMessage(block))
		}
	}

	params := anthropic.BetaMessageNewParams{
		Model:     p.s.Model,
		MaxTokens: anthropicMaxTokens,
		Messages:  msgs,
	}
	if system != "" {
		// The system prompt and the tool list are static, so one cache breakpoint on the
		// system block caches both across every turn of every conversation.
		params.System = []anthropic.BetaTextBlockParam{{Text: system, CacheControl: anthropic.NewBetaCacheControlEphemeralParam()}}
	}
	for _, t := range tools {
		tp := anthropic.BetaToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.BetaToolInputSchemaParam{Properties: t.Parameters["properties"], Required: requiredOf(t.Parameters)},
		}
		if p.official {
			// Stream tool arguments as they are generated; Agent validates them before use.
			tp.EagerInputStreaming = anthropic.Bool(true)
		}
		params.Tools = append(params.Tools, anthropic.BetaToolUnionParam{OfTool: &tp})
	}
	if p.official && slices.Contains(serverFallbackModels, p.s.Model) {
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
		params.Betas = append(params.Betas, anthropic.AnthropicBetaServerSideFallback2026_07_01)
	}
	return &anthropicSession{client: p.client, params: params}
}

func (p *anthropicProvider) ListModels(ctx context.Context) ([]string, error) {
	var ids []string
	pager := p.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	for pager.Next() {
		ids = append(ids, pager.Current().ID)
	}
	return ids, pager.Err()
}

type anthropicSession struct {
	client anthropic.Client
	params anthropic.BetaMessageNewParams
}

func (s *anthropicSession) Step(ctx context.Context, emit func(Event)) ([]ToolCall, error) {
	stream := s.client.Beta.Messages.NewStreaming(ctx, s.params)
	defer func() { _ = stream.Close() }()

	var msg anthropic.BetaMessage
	for stream.Next() {
		event := stream.Current()
		if err := msg.Accumulate(event); err != nil {
			return nil, fmt.Errorf("read Anthropic stream: %w", err)
		}
		switch ev := event.AsAny().(type) {
		case anthropic.BetaRawContentBlockStartEvent:
			// A server-side fallback took over after a mid-stream refusal: what was
			// streamed so far is the refused partial answer and must be discarded.
			if _, ok := ev.ContentBlock.AsAny().(anthropic.BetaFallbackBlock); ok {
				emit(Event{Type: "reset"})
			}
		case anthropic.BetaRawContentBlockDeltaEvent:
			if d, ok := ev.Delta.AsAny().(anthropic.BetaTextDelta); ok && d.Text != "" {
				emit(Event{Type: "text", Text: d.Text})
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, describeAnthropicError(err)
	}

	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		// A refusal can cut a tool call off mid-input: never run that turn's tools.
		return nil, ErrRefused
	case anthropic.BetaStopReasonMaxTokens:
		return nil, errors.New("the answer hit the output token limit")
	}

	s.params.Messages = append(s.params.Messages, msg.ToParam())
	if msg.StopReason != anthropic.BetaStopReasonToolUse {
		return nil, nil
	}

	var calls []ToolCall
	for _, block := range msg.Content {
		tu, ok := block.AsAny().(anthropic.BetaToolUseBlock)
		if !ok {
			continue
		}
		raw := tu.JSON.Input.Raw()
		call := ToolCall{ID: tu.ID, Name: tu.Name, RawArgs: raw}
		// Strict parse: with eager input streaming the API no longer validates arguments.
		if err := json.Unmarshal([]byte(raw), &call.Args); err != nil || call.Args == nil {
			call.Invalid = fmt.Errorf("arguments are not a JSON object")
			call.Args = nil
		}
		calls = append(calls, call)
	}
	return calls, nil
}

func (s *anthropicSession) AddResults(results []ToolResult) {
	blocks := make([]anthropic.BetaContentBlockParamUnion, 0, len(results))
	for _, r := range results {
		blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.Call.ID, r.Content, r.IsError))
	}
	// All results of one turn go back in a single user message.
	s.params.Messages = append(s.params.Messages, anthropic.NewBetaUserMessage(blocks...))
}

// describeAnthropicError turns SDK errors into messages an operator can act on.
func describeAnthropicError(err error) error {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("could not reach Anthropic: %w", err)
	}
	switch apiErr.StatusCode {
	case 401, 403:
		return errors.New("the Anthropic API rejected the API key")
	case 404:
		return errors.New("the Anthropic API does not know the configured model; pick one from the model list")
	case 429:
		return errors.New("the Anthropic API rate limit was reached; try again shortly")
	}
	if apiErr.StatusCode >= 500 {
		return fmt.Errorf("the Anthropic API is temporarily unavailable (HTTP %d)", apiErr.StatusCode)
	}
	return fmt.Errorf("the Anthropic API rejected the request (HTTP %d): %s", apiErr.StatusCode, apiErr.Error())
}
