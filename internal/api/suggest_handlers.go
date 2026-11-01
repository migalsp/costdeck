package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/pricing"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// suggestWindow is how much usage history a suggestion rests on: a week, in hourly steps.
const suggestWindow = 7 * 24 * time.Hour

// suggestionResponse is GET /api/namespaces/{ns}/schedule-suggestion.
type suggestionResponse struct {
	scaling.Suggestion
	Namespace string `json:"namespace"`
	// MonthlySaving is what the suggested schedule would keep down at the namespace's
	// current cost; Currency its currency.
	MonthlySaving float64 `json:"monthlySaving"`
	Currency      string  `json:"currency"`
	// Source says where the history came from: victoriametrics, or costdeck (the last hour
	// only, never enough).
	Source string `json:"source"`
}

func (s *Server) handleScheduleSuggestion(w http.ResponseWriter, r *http.Request) {
	tz := r.URL.Query().Get("timezone")
	if tz == "" {
		tz = "UTC"
	}
	resp, err := s.scheduleSuggestion(r.Context(), r.PathValue("ns"), tz)
	if err != nil {
		if _, ok := errors.AsType[badRequest](err); ok {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeK8sError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) scheduleSuggestion(ctx context.Context, ns, tz string) (suggestionResponse, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return suggestionResponse{}, badRequestf("unknown timezone %q", tz)
	}
	resp := suggestionResponse{Namespace: ns, Source: "costdeck"}
	var points []finopsv1.MetricDataPoint
	if s.Metrics != nil {
		history, _, err := s.Metrics.NamespaceHistory(ctx, suggestWindow)
		switch {
		case err == nil:
			resp.Source, points = metrics.SourceVictoriaMetrics, history[ns]
		case !errors.Is(err, metrics.ErrNoHistory):
			return resp, err
		}
	}
	resp.Suggestion = scaling.SuggestSchedule(points, metrics.HistoryStep(suggestWindow), loc)
	snap, err := finops.Build(ctx, s.Client, s.costRates(ctx), finops.Options{Namespace: config.OperatorNamespace(), Now: time.Now(), Live: s.APIReader})
	if err != nil {
		return resp, err
	}
	resp.Currency = snap.Rates.Currency
	if resp.Reason == scaling.SuggestPattern {
		for _, n := range snap.Namespaces {
			if n.Name == ns {
				resp.MonthlySaving = n.RequestedHourly * pricing.HoursPerMonth * (1 - resp.UpShare)
			}
		}
	}
	return resp, nil
}

// toolSuggestSchedule is the suggestion for the assistant and MCP.
func (s *Server) toolSuggestSchedule(ctx context.Context, ns, tz string) (string, error) {
	if tz == "" {
		tz = "UTC"
	}
	resp, err := s.scheduleSuggestion(ctx, ns, tz)
	if err != nil {
		return "", err
	}
	note := map[string]string{
		scaling.SuggestPattern:      "Busy and idle hours are clear: these windows keep it up while it is used, with an hour of margin.",
		scaling.SuggestSteady:       "It is busy at almost every hour: a schedule would cut into use.",
		scaling.SuggestIdle:         "It did nothing for the whole week: it could stay scaled down, or be removed.",
		scaling.SuggestInsufficient: "Not enough usage history: connect VictoriaMetrics, which keeps more than the last hour.",
	}[resp.Reason]
	return toJSON(map[string]any{
		"namespace": ns, "reason": resp.Reason, "note": note, "schedules": resp.Schedules,
		"upShare": fmt.Sprintf("%.0f%%", resp.UpShare*100), "monthlySaving": round2(resp.MonthlySaving), "currency": resp.Currency,
		"historyDays": resp.Days, "timezone": resp.Timezone,
	})
}
