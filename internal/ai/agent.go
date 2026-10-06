// Package ai runs CostDeck's assistant: a provider-agnostic tool-calling loop over
// Anthropic (official SDK), OpenAI-compatible endpoints (OpenAI, Azure OpenAI gateways,
// Ollama, vLLM) and Google Gemini.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Conversation roles.
const (
	roleUser      = "user"
	roleAssistant = "assistant"
)

// Message is one turn of the conversation as the dashboard keeps it: plain text only.
// Thinking blocks and tool rounds are never replayed across requests, so the history the
// client sends back cannot invalidate anything the provider bound to an earlier request.
type Message struct {
	Role    string `json:"role"` // user or assistant
	Content string `json:"content"`
}

// Tool is a function the model may call.
type Tool struct {
	Name        string
	Description string
	// Parameters is the JSON Schema of the arguments: {"type":"object","properties":...}.
	Parameters map[string]any
	// Mutating tools change the cluster. The model never runs them directly: the call is
	// turned into a confirmation card and the user executes it from the dashboard.
	Mutating bool
	// Run executes a read-only tool. Arguments have been validated against Parameters.
	Run func(ctx context.Context, args map[string]any) (string, error)
}

// Event is streamed to the client while the agent works.
type Event struct {
	// Type is text, reset, tool_call, tool_result, confirm, error or done.
	Type    string         `json:"type"`
	Text    string         `json:"text,omitempty"`
	Tool    string         `json:"tool,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	IsError bool           `json:"isError,omitempty"`
}

// ToolCall is a call the model requested.
type ToolCall struct {
	ID      string
	Name    string
	Args    map[string]any
	RawArgs string
	// Invalid is set when the arguments were not a JSON object.
	Invalid error
}

// ToolResult answers one ToolCall.
type ToolResult struct {
	Call    ToolCall
	Content string
	IsError bool
}

// session is one conversation in a provider's native representation.
type session interface {
	// Step runs one model call, streaming text through emit. It returns the tool calls the
	// model made; none means the model finished its answer.
	Step(ctx context.Context, emit func(Event)) ([]ToolCall, error)
	// AddResults appends the answers to the calls returned by the last Step.
	AddResults(results []ToolResult)
}

// Provider creates sessions and lists the models an account can use.
type Provider interface {
	NewSession(system string, history []Message, tools []Tool) session
	ListModels(ctx context.Context) ([]string, error)
}

// ErrRefused is returned when the model (or a safety classifier) declined the request.
var ErrRefused = errors.New("the model declined to answer this request")

// Agent drives the tool-calling loop.
type Agent struct {
	Provider Provider
	System   string
	Tools    []Tool
	MaxSteps int
}

// confirmationNote is what the model sees after asking for a mutating action.
const confirmationNote = "CostDeck showed the user a confirmation card for this action. It has NOT been executed yet. " +
	"Do not call the tool again; tell the user in one or two sentences what will happen when they confirm."

// Run answers the last user message in history, streaming events through emit.
func (a *Agent) Run(ctx context.Context, history []Message, emit func(Event)) error {
	maxSteps := a.MaxSteps
	if maxSteps == 0 {
		maxSteps = 8
	}
	byName := make(map[string]Tool, len(a.Tools))
	for _, t := range a.Tools {
		byName[t.Name] = t
	}

	s := a.Provider.NewSession(a.System, history, a.Tools)
	for range maxSteps {
		calls, err := s.Step(ctx, emit)
		if err != nil {
			return err
		}
		if len(calls) == 0 {
			return nil
		}
		results := make([]ToolResult, 0, len(calls))
		for _, call := range calls {
			results = append(results, a.execute(ctx, byName, call, emit))
		}
		s.AddResults(results)
	}
	return fmt.Errorf("stopped after %d tool steps without a final answer", maxSteps)
}

func (a *Agent) execute(ctx context.Context, tools map[string]Tool, call ToolCall, emit func(Event)) ToolResult {
	fail := func(msg string) ToolResult {
		emit(Event{Type: "tool_result", Tool: call.Name, Text: msg, IsError: true})
		return ToolResult{Call: call, Content: msg, IsError: true}
	}

	tool, ok := tools[call.Name]
	if !ok {
		return fail(fmt.Sprintf("unknown tool %q", call.Name))
	}
	if call.Invalid != nil {
		raw, _ := json.Marshal(map[string]string{"INVALID_JSON": call.RawArgs})
		return fail(string(raw))
	}
	if err := ValidateArgs(tool.Parameters, call.Args); err != nil {
		return fail("invalid arguments: " + err.Error())
	}

	emit(Event{Type: "tool_call", Tool: call.Name, Args: call.Args})
	if tool.Mutating {
		emit(Event{Type: "confirm", Tool: call.Name, Args: call.Args})
		return ToolResult{Call: call, Content: confirmationNote}
	}

	out, err := tool.Run(ctx, call.Args)
	if err != nil {
		return fail(err.Error())
	}
	emit(Event{Type: "tool_result", Tool: call.Name, Text: summarize(out)})
	return ToolResult{Call: call, Content: out}
}

// summarize shortens a tool result for display; the model always gets the full text.
func summarize(s string) string {
	s = strings.TrimSpace(s)
	if line, _, ok := strings.Cut(s, "\n"); ok {
		s = line + " …"
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// ValidateArgs checks arguments against the subset of JSON Schema CostDeck tools use:
// required properties, primitive types and enums. Unknown properties are rejected so a
// hallucinated argument cannot silently change behaviour.
func ValidateArgs(schema map[string]any, args map[string]any) error {
	props, _ := schema["properties"].(map[string]any)
	for _, req := range requiredOf(schema) {
		if v, ok := args[req]; !ok || v == nil || v == "" {
			return fmt.Errorf("missing required argument %q", req)
		}
	}
	for name, value := range args {
		spec, ok := props[name].(map[string]any)
		if !ok {
			return fmt.Errorf("unknown argument %q", name)
		}
		if err := checkType(name, spec, value); err != nil {
			return err
		}
	}
	return nil
}

func requiredOf(schema map[string]any) []string {
	switch r := schema["required"].(type) {
	case []string:
		return r
	case []any:
		out := make([]string, 0, len(r))
		for _, v := range r {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func checkType(name string, spec map[string]any, value any) error {
	switch spec["type"] {
	case "string":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("argument %q must be a string", name)
		}
		if enum, ok := spec["enum"].([]string); ok && len(enum) > 0 && !slices.Contains(enum, s) {
			return fmt.Errorf("argument %q must be one of %s", name, strings.Join(enum, ", "))
		}
	case "integer", "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("argument %q must be a number", name)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("argument %q must be a boolean", name)
		}
	}
	return nil
}

// sanitizeHistory keeps the conversation well-formed for every provider: only user and
// assistant turns with content, starting with a user turn, the most recent turns only.
func sanitizeHistory(history []Message) []Message {
	const maxTurns, maxChars = 24, 16000
	out := make([]Message, 0, len(history))
	for _, m := range history {
		role := strings.ToLower(m.Role)
		text := strings.TrimSpace(m.Content)
		if (role != roleUser && role != roleAssistant) || text == "" {
			continue
		}
		if len(text) > maxChars {
			text = text[:maxChars]
		}
		// Merge consecutive turns of the same role; some providers reject them.
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content += "\n\n" + text
			continue
		}
		out = append(out, Message{Role: role, Content: text})
	}
	if len(out) > maxTurns {
		out = out[len(out)-maxTurns:]
	}
	for len(out) > 0 && out[0].Role != roleUser {
		out = out[1:]
	}
	return out
}
