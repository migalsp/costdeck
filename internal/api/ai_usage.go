package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/ai"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
)

// The assistant's token usage is kept per calendar month (UTC) in a ConfigMap in the
// operator namespace, so every replica counts against the same budget.

const (
	aiUsageConfigMap = "costdeck-ai-usage"
	aiUsageMonths    = 12
)

// AIUsage is what the assistant and AI reports consumed in one month.
type AIUsage struct {
	Month        string `json:"month"` // YYYY-MM
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	Requests     int64  `json:"requests"`
	// Estimated is set when part of it was guessed from the text: the provider did not
	// report usage.
	Estimated bool `json:"estimated,omitempty"`
}

// Tokens is input and output together, what the budget counts.
func (u AIUsage) Tokens() int64 { return u.InputTokens + u.OutputTokens }

func usageMonth(now time.Time) string { return now.UTC().Format("2006-01") }

// aiUsage reads the usage of the month now falls in.
func (s *Server) aiUsage(ctx context.Context, now time.Time) (AIUsage, error) {
	month := usageMonth(now)
	var cm corev1.ConfigMap
	err := s.Client.Get(ctx, client.ObjectKey{Namespace: config.OperatorNamespace(), Name: aiUsageConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		return AIUsage{Month: month}, nil
	}
	if err != nil {
		return AIUsage{}, err
	}
	u := AIUsage{Month: month}
	if raw, ok := cm.Data[month]; ok {
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			return AIUsage{Month: month}, nil // a damaged entry starts the month afresh
		}
	}
	return u, nil
}

// addAIUsage adds one request's usage to its month, dropping months older than a year.
func (s *Server) addAIUsage(ctx context.Context, now time.Time, used ai.Usage) error {
	month := usageMonth(now)
	key := client.ObjectKey{Namespace: config.OperatorNamespace(), Name: aiUsageConfigMap}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cm corev1.ConfigMap
		err := s.Client.Get(ctx, key, &cm)
		create := apierrors.IsNotFound(err)
		if err != nil && !create {
			return err
		}
		if create {
			cm = corev1.ConfigMap{Name: key.Name, Namespace: key.Namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "costdeck"}}
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		u := AIUsage{Month: month}
		if raw, ok := cm.Data[month]; ok {
			_ = json.Unmarshal([]byte(raw), &u)
		}
		u.InputTokens += used.InputTokens
		u.OutputTokens += used.OutputTokens
		u.Requests++
		u.Estimated = u.Estimated || used.Estimated
		raw, err := json.Marshal(u)
		if err != nil {
			return err
		}
		cm.Data[month] = string(raw)
		months := make([]string, 0, len(cm.Data))
		for m := range cm.Data {
			months = append(months, m)
		}
		sort.Strings(months)
		for len(months) > aiUsageMonths {
			delete(cm.Data, months[0])
			months = months[1:]
		}
		if create {
			return s.Client.Create(ctx, &cm)
		}
		return s.Client.Update(ctx, &cm)
	})
}

// usageMeter sums the usage events of one request.
type usageMeter struct {
	total ai.Usage
	calls int
}

// observe wraps an event sink: usage events are counted, and kept from the browser.
func (m *usageMeter) observe(next func(ai.Event)) func(ai.Event) {
	return func(e ai.Event) {
		if e.Type == "usage" && e.Usage != nil {
			m.total.InputTokens += e.Usage.InputTokens
			m.total.OutputTokens += e.Usage.OutputTokens
			m.total.Estimated = m.total.Estimated || e.Usage.Estimated
			m.calls++
			return
		}
		next(e)
	}
}

// usageCost prices a month's usage at the model's price, or nil without one.
func usageCost(u AIUsage, model string, prices []finopsv1.LLMModelPrice) *float64 {
	p, ok := finops.ModelPrice(prices, model)
	if !ok {
		return nil
	}
	cost := (float64(u.InputTokens)*p[0] + float64(u.OutputTokens)*p[1]) / 1e6
	return &cost
}

// checkAIBudget refuses a request once the month's token budget is spent, answering it
// itself. A budget it cannot check does not block the assistant.
func (s *Server) checkAIBudget(w http.ResponseWriter, r *http.Request) bool {
	cfg, err := config.Get(r.Context(), s.Client)
	if err != nil || cfg.Spec.Integrations.AI == nil || cfg.Spec.Integrations.AI.MonthlyTokenBudget <= 0 {
		return true
	}
	budget := cfg.Spec.Integrations.AI.MonthlyTokenBudget
	usage, err := s.aiUsage(r.Context(), time.Now())
	if err != nil {
		logf.FromContext(r.Context()).Error(err, "Could not read the assistant's token usage")
		return true
	}
	if usage.Tokens() >= budget {
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf(
			"The assistant has used its budget of %d tokens for %s. It answers again next month, or when an admin raises the budget under Settings → AI.", budget, usage.Month))
		return false
	}
	return true
}

// recordAIUsage adds what a request used to its month, even when the client went away.
func (s *Server) recordAIUsage(ctx context.Context, m *usageMeter) {
	if m.calls == 0 {
		return
	}
	if err := s.addAIUsage(context.WithoutCancel(ctx), time.Now(), m.total); err != nil {
		logf.FromContext(ctx).Error(err, "Could not record the assistant's token usage")
	}
}
