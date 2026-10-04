package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/ai"
	"github.com/migalsp/costdeck-operator/internal/auth"
	"github.com/migalsp/costdeck-operator/internal/config"
)

// eventStream writes ai.Events as server-sent events.
type eventStream struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

func newEventStream(w http.ResponseWriter) (*eventStream, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// ingress-nginx and other proxies buffer responses unless told otherwise.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	return &eventStream{w: w, f: f}, true
}

func (s *eventStream) send(e ai.Event) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", data)
	s.f.Flush()
}

// userMessage turns agent errors into something the chat can show.
func userMessage(err error) string {
	switch {
	case errors.Is(err, ai.ErrRefused):
		return "The model declined to answer this request. Rephrase it, or pick another model under Settings → AI Models."
	case errors.Is(err, context.Canceled):
		return "The request was cancelled."
	default:
		return err.Error()
	}
}

// loadAIProvider resolves the configured provider, answering the request itself when the
// integration is off or misconfigured.
func (s *Server) loadAIProvider(w http.ResponseWriter, r *http.Request) (ai.Provider, bool) {
	settings, err := ai.LoadSettings(r.Context(), s.Client)
	if errors.Is(err, ai.ErrDisabled) {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "AI is misconfigured: "+err.Error())
		return nil, false
	}
	provider, err := ai.NewProvider(settings)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	return provider, true
}

// canMutate reports whether the caller may confirm cluster changes.
func canMutate(r *http.Request) bool {
	id := auth.FromContext(r.Context())
	return id == nil || id.Role.Allows(auth.RoleOperator)
}

// handleAIChat streams an assistant answer: POST /api/ai/chat {"messages": [...]}.
func (s *Server) handleAIChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []ai.Message `json:"messages"`
	}
	if err := decodeJSON(w, r, &req); err != nil || len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "expected {\"messages\": [{\"role\": \"user\", \"content\": \"...\"}]}")
		return
	}
	provider, ok := s.loadAIProvider(w, r)
	if !ok {
		return
	}
	stream, ok := newEventStream(w)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	agent := &ai.Agent{Provider: provider, System: assistantSystemPrompt, Tools: s.toolsFor(canMutate(r))}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if err := agent.Run(ctx, req.Messages, stream.send); err != nil {
		logf.FromContext(ctx).Error(err, "Assistant request failed")
		stream.send(ai.Event{Type: "error", Text: userMessage(err)})
	}
	stream.send(ai.Event{Type: "done"})
}

// handleAIExecuteTool runs an action the user confirmed: POST /api/ai/tools/{name}.
func (s *Server) handleAIExecuteTool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Args map[string]any `json:"args"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Args == nil {
		req.Args = map[string]any{}
	}
	name := r.PathValue("name")
	result, err := s.executeMutatingTool(r.Context(), name, req.Args)
	if err != nil {
		var bad errBadOverride
		if errors.As(err, &bad) {
			writeError(w, http.StatusBadRequest, bad.Error())
			return
		}
		writeK8sError(w, err)
		return
	}
	who := "unknown"
	if id := auth.FromContext(r.Context()); id != nil {
		who = id.Subject
	}
	logf.FromContext(r.Context()).Info("Executed assistant action", "action", name, "args", req.Args, "user", who)
	writeJSON(w, http.StatusOK, map[string]string{"result": result})
}

// aiTestRequest carries unsaved AI settings from the settings form.
type aiTestRequest struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	BaseURL       string `json:"baseUrl"`
	APIKey        string `json:"apiKey"`
	SkipSSLVerify *bool  `json:"skipSslVerify"`
}

// settingsFromRequest merges unsaved form values over the stored AI configuration.
func (s *Server) settingsFromRequest(ctx context.Context, req aiTestRequest) (*ai.Settings, error) {
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		return nil, err
	}
	stored := finopsv1.AIIntegrationConfig{}
	if cfg.Spec.Integrations.AI != nil {
		stored = *cfg.Spec.Integrations.AI
	}
	merged := stored
	if req.Provider != "" {
		merged.Provider = req.Provider
		if !strings.EqualFold(req.Provider, stored.Provider) {
			// The stored key belongs to another provider.
			merged.SecretRef = ""
		}
	}
	if req.Model != "" {
		merged.Model = req.Model
	}
	merged.BaseURL = req.BaseURL
	if req.SkipSSLVerify != nil {
		merged.SkipSSLVerify = *req.SkipSSLVerify
	}
	return ai.FromConfig(ctx, s.Client, &merged, req.APIKey)
}

// handleAIModels lists the models the configured account can use:
// POST /api/settings/ai/models with optional unsaved settings.
func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request) {
	var req aiTestRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	settings, err := s.settingsFromRequest(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider, err := ai.NewProvider(settings)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	models, err := provider.ListModels(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models, "default": ai.DefaultModels[settings.Provider]})
}

// testAIProvider validates AI settings by listing the account's models, which proves the
// endpoint is reachable and the key is accepted, and checks the chosen model is offered.
func (s *Server) testAIProvider(w http.ResponseWriter, r *http.Request) {
	var req aiTestRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	fail := func(err error) { writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()}) }
	settings, err := s.settingsFromRequest(r.Context(), req)
	if err != nil {
		fail(err)
		return
	}
	provider, err := ai.NewProvider(settings)
	if err != nil {
		fail(err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	models, err := provider.ListModels(ctx)
	if err != nil {
		fail(err)
		return
	}
	msg := fmt.Sprintf("Connected — %d models available", len(models))
	found := false
	for _, m := range models {
		if m == settings.Model {
			found = true
		}
	}
	if len(models) > 0 && !found {
		msg += fmt.Sprintf("; %q is not among them", settings.Model)
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "message": msg, "modelAvailable": found || len(models) == 0})
}
