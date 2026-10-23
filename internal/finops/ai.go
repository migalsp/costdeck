package finops

import (
	"maps"
	"path"
	"slices"
	"sort"
	"strconv"
	"time"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

// AI costs are what the cluster's applications pay LLM APIs for tokens, by team and model,
// and what the models the cluster serves itself cost per million tokens. Token counts
// cover metrics.LLMWindow and are extrapolated to a month.

// Sources of LLM API spend.
const (
	SourceLiteLLM       = "litellm"
	SourceOpenTelemetry = "opentelemetry"
	unassigned          = "unassigned"
)

// AICosts is the AI cost page.
type AICosts struct {
	Currency string `json:"currency"`
	// WindowDays is how many days the token counts cover.
	WindowDays int      `json:"windowDays"`
	API        APISpend `json:"api"`
	// SelfHosted are the models vLLM serves in the cluster.
	SelfHosted []SelfHostedModel `json:"selfHosted"`
}

// APISpend is what applications pay LLM APIs.
type APISpend struct {
	Monthly float64   `json:"monthly"`
	Sources []string  `json:"sources"`
	Teams   []AITeam  `json:"teams"`
	Models  []AIModel `json:"models"`
	// Unpriced are models applications use that no price covers (Settings -> Prices).
	Unpriced []string `json:"unpriced"`
}

// AITeam is one team's LLM spend: a LiteLLM team, or the team of the namespaces whose
// applications report OpenTelemetry metrics.
type AITeam struct {
	Team string `json:"team"`
	// Source is litellm or opentelemetry.
	Source  string  `json:"source"`
	Monthly float64 `json:"monthly"`
	// InputTokens and OutputTokens are per month.
	InputTokens  float64  `json:"inputTokens"`
	OutputTokens float64  `json:"outputTokens"`
	Namespaces   []string `json:"namespaces,omitempty"`
}

// AIModel is one model's LLM spend across teams.
type AIModel struct {
	Model        string  `json:"model"`
	Monthly      float64 `json:"monthly"`
	InputTokens  float64 `json:"inputTokens"`
	OutputTokens float64 `json:"outputTokens"`
	Priced       bool    `json:"priced"`
}

// SelfHostedModel is a model vLLM serves in a namespace, and what its tokens cost at the
// namespace's run rate (its share of it, when the namespace serves several models).
type SelfHostedModel struct {
	Namespace     string  `json:"namespace"`
	Model         string  `json:"model"`
	TokensPerHour float64 `json:"tokensPerHour"`
	MonthlyCost   float64 `json:"monthlyCost"`
	// CostPer1M is what a million tokens (prompt and generated) cost; absent without
	// tokens.
	CostPer1M *float64 `json:"costPer1M,omitempty"`
}

// BuildAICosts prices the LLM usage: LiteLLM spend as reported, OpenTelemetry tokens at the
// model prices, and served tokens at the cost of the namespace that serves them.
func BuildAICosts(u metrics.LLMUsage, s Snapshot, prices []finopsv1.LLMModelPrice) AICosts {
	windowHours := metrics.LLMWindow.Hours()
	month := pricing.HoursPerMonth / windowHours
	out := AICosts{Currency: s.Rates.Currency, WindowDays: int(metrics.LLMWindow / (24 * time.Hour)),
		API: APISpend{Sources: []string{}, Teams: []AITeam{}, Models: []AIModel{}, Unpriced: []string{}}, SelfHosted: []SelfHostedModel{}}

	teams := map[[2]string]*AITeam{}
	models := map[string]*AIModel{}
	team := func(name, source string) *AITeam {
		if name == "" {
			name = unassigned
		}
		key := [2]string{name, source}
		if teams[key] == nil {
			teams[key] = &AITeam{Team: name, Source: source}
		}
		return teams[key]
	}
	model := func(name string, priced bool) *AIModel {
		if models[name] == nil {
			models[name] = &AIModel{Model: name, Priced: priced}
		}
		models[name].Priced = models[name].Priced && priced
		return models[name]
	}

	for _, p := range u.Proxy {
		t, m := team(p.Team, SourceLiteLLM), model(p.Model, true)
		t.Monthly += p.Spend * month
		t.InputTokens += p.Input * month
		t.OutputTokens += p.Output * month
		m.Monthly += p.Spend * month
		m.InputTokens += p.Input * month
		m.OutputTokens += p.Output * month
	}
	// Any namespace's labels name its team, tracked or not.
	labels := map[string]map[string]string{}
	for _, ns := range s.Namespaces {
		labels[ns.Name] = ns.Labels
	}
	maps.Copy(labels, s.Labels)
	for _, c := range u.Clients {
		price, priced := modelPrice(prices, c.Model)
		cost := (c.Input*price[0] + c.Output*price[1]) / 1e6 * month
		t := team(Team(labels[c.Namespace]), SourceOpenTelemetry)
		t.Monthly += cost
		t.InputTokens += c.Input * month
		t.OutputTokens += c.Output * month
		if c.Namespace != "" && !slices.Contains(t.Namespaces, c.Namespace) {
			t.Namespaces = append(t.Namespaces, c.Namespace)
			sort.Strings(t.Namespaces)
		}
		m := model(c.Model, priced)
		m.Monthly += cost
		m.InputTokens += c.Input * month
		m.OutputTokens += c.Output * month
		if !priced && !slices.Contains(out.API.Unpriced, c.Model) {
			out.API.Unpriced = append(out.API.Unpriced, c.Model)
		}
	}
	if len(u.Proxy) > 0 {
		out.API.Sources = append(out.API.Sources, SourceLiteLLM)
	}
	if len(u.Clients) > 0 {
		out.API.Sources = append(out.API.Sources, SourceOpenTelemetry)
	}
	for _, t := range teams {
		out.API.Teams = append(out.API.Teams, *t)
		out.API.Monthly += t.Monthly
	}
	for _, m := range models {
		out.API.Models = append(out.API.Models, *m)
	}
	sort.Slice(out.API.Teams, func(i, j int) bool {
		if out.API.Teams[i].Monthly != out.API.Teams[j].Monthly {
			return out.API.Teams[i].Monthly > out.API.Teams[j].Monthly
		}
		return out.API.Teams[i].Team < out.API.Teams[j].Team
	})
	sort.Slice(out.API.Models, func(i, j int) bool {
		if out.API.Models[i].Monthly != out.API.Models[j].Monthly {
			return out.API.Models[i].Monthly > out.API.Models[j].Monthly
		}
		return out.API.Models[i].Model < out.API.Models[j].Model
	})
	sort.Strings(out.API.Unpriced)

	// A namespace's cost is shared by the models it serves in proportion to their tokens.
	hourly := map[string]float64{}
	for _, ns := range s.Namespaces {
		hourly[ns.Name] = ns.RequestedHourly
	}
	tokensIn := map[string]float64{}
	for _, sv := range u.Served {
		tokensIn[sv.Namespace] += sv.Input + sv.Output
	}
	for _, sv := range u.Served {
		tokens := sv.Input + sv.Output
		share := 0.0
		if tokensIn[sv.Namespace] > 0 {
			share = tokens / tokensIn[sv.Namespace]
		}
		m := SelfHostedModel{Namespace: sv.Namespace, Model: sv.Model, TokensPerHour: tokens / windowHours,
			MonthlyCost: hourly[sv.Namespace] * share * pricing.HoursPerMonth}
		if tokens > 0 {
			per := hourly[sv.Namespace] * share * windowHours / tokens * 1e6
			m.CostPer1M = &per
		}
		out.SelfHosted = append(out.SelfHosted, m)
	}
	sort.Slice(out.SelfHosted, func(i, j int) bool {
		if out.SelfHosted[i].MonthlyCost != out.SelfHosted[j].MonthlyCost {
			return out.SelfHosted[i].MonthlyCost > out.SelfHosted[j].MonthlyCost
		}
		return out.SelfHosted[i].Namespace+out.SelfHosted[i].Model < out.SelfHosted[j].Namespace+out.SelfHosted[j].Model
	})
	return out
}

// modelPrice returns the input and output price per million tokens of the first entry
// whose model or glob matches.
func modelPrice(prices []finopsv1.LLMModelPrice, model string) ([2]float64, bool) {
	for _, p := range prices {
		if ok, err := path.Match(p.Model, model); err != nil || !ok {
			continue
		}
		in, errIn := strconv.ParseFloat(p.InputPer1M, 64)
		outP, errOut := strconv.ParseFloat(p.OutputPer1M, 64)
		if errIn == nil && errOut == nil {
			return [2]float64{in, outP}, true
		}
	}
	return [2]float64{}, false
}
