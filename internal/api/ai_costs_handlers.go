package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
	"github.com/migalsp/costdeck-operator/internal/metrics"
)

// aiCostsResponse is GET /api/finops/ai. Available is false without VictoriaMetrics,
// where the token counts come from.
type aiCostsResponse struct {
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
	finops.AICosts
}

func (s *Server) handleAICosts(w http.ResponseWriter, r *http.Request) {
	resp, err := s.aiCosts(r.Context())
	if err != nil {
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// aiCosts prices the cluster's LLM traffic. Failing to read the token counts is reported
// in the answer, not as an error: the page still explains where they would come from.
func (s *Server) aiCosts(ctx context.Context) (aiCostsResponse, error) {
	snap, err := finops.Build(ctx, s.Client, s.costRates(ctx), finops.Options{Namespace: config.OperatorNamespace(), Now: time.Now(), Live: s.APIReader})
	if err != nil {
		return aiCostsResponse{}, err
	}
	cfg, err := config.Get(ctx, s.Client)
	if err != nil {
		return aiCostsResponse{}, err
	}
	var usage metrics.LLMUsage
	resp := aiCostsResponse{}
	if s.Metrics != nil {
		usage, err = s.Metrics.LLMUsage(ctx)
		switch {
		case err == nil:
			resp.Available = true
		case errors.Is(err, metrics.ErrNoHistory):
		default:
			resp.Error = err.Error()
		}
	}
	resp.AICosts = finops.BuildAICosts(usage, snap, cfg.Spec.Pricing.LLMModels)
	return resp, nil
}

// toolAICosts is the AI cost page for the assistant and MCP.
func (s *Server) toolAICosts(ctx context.Context) (string, error) {
	resp, err := s.aiCosts(ctx)
	if err != nil {
		return "", err
	}
	if !resp.Available && resp.Error == "" {
		resp.Error = "VictoriaMetrics is not connected: token counts come from LiteLLM, OpenTelemetry GenAI and vLLM metrics scraped into it."
	}
	return toJSON(resp)
}
