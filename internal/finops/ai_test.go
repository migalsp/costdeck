package finops

import (
	"strings"
	"testing"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/metrics"
	"github.com/migalsp/costdeck-operator/internal/pricing"
)

const usd = "USD"

func TestBuildAICosts(t *testing.T) {
	// A month is 730/168 of the seven days the counts cover.
	month := 730.0 / 168
	// checkout is not tracked (no usage yet): its namespace labels still name its team.
	snap := Snapshot{Rates: pricing.Rates{Currency: usd}, Namespaces: []Namespace{
		{Name: "shop", Labels: map[string]string{"team": "payments"}},
		{Name: "llm", RequestedHourly: 1.68},
	}, Labels: map[string]map[string]string{"checkout": {"team": "payments"}}}
	usage := metrics.LLMUsage{
		Proxy: []metrics.ProxyUsage{{Team: "search", Model: "gpt-4o", Spend: 10, Input: 1e6, Output: 2e5}, {Model: "gpt-4o", Spend: 2}},
		Clients: []metrics.TokenUsage{
			{Namespace: "shop", Model: "claude-sonnet-4-5", Input: 2e6, Output: 1e6},
			{Namespace: "checkout", Model: "claude-haiku-4-5", Input: 1e6},
			{Namespace: "shop", Model: "mystery-model", Input: 5e5},
		},
		Served: []metrics.TokenUsage{{Namespace: "llm", Model: "llama", Input: 3e6, Output: 1e6}, {Namespace: "llm", Model: "embed", Input: 4e6}},
	}
	prices := []finopsv1.LLMModelPrice{
		{Model: "claude-sonnet-*", InputPer1M: "3", OutputPer1M: "15"},
		{Model: "claude-*", InputPer1M: "1", OutputPer1M: "5"},
	}
	got := BuildAICosts(usage, snap, prices)
	if got.WindowDays != 7 || got.Currency != usd || strings.Join(got.API.Sources, ",") != "litellm,opentelemetry" {
		t.Errorf("costs = %+v", got)
	}
	// payments: shop's Sonnet (2×3 + 1×15 = 21) and checkout's Haiku (1×1 = 1); the unpriced model adds tokens only.
	want := map[string]float64{"payments": 22 * month, "search": 10 * month, "unassigned": 2 * month}
	if len(got.API.Teams) != 3 {
		t.Fatalf("teams = %+v", got.API.Teams)
	}
	for _, team := range got.API.Teams {
		if !near(team.Monthly, want[team.Team]) {
			t.Errorf("%s = %v/mo, want %v", team.Team, team.Monthly, want[team.Team])
		}
	}
	payments := got.API.Teams[0]
	if payments.Team != "payments" || payments.Source != SourceOpenTelemetry || strings.Join(payments.Namespaces, ",") != "checkout,shop" || !near(payments.InputTokens, 3.5e6*month) {
		t.Errorf("payments = %+v", payments)
	}
	if !near(got.API.Monthly, 34*month) {
		t.Errorf("monthly = %v", got.API.Monthly)
	}
	if strings.Join(got.API.Unpriced, ",") != "mystery-model" {
		t.Errorf("unpriced = %v", got.API.Unpriced)
	}
	models := map[string]AIModel{}
	for _, m := range got.API.Models {
		models[m.Model] = m
	}
	if !near(models["gpt-4o"].Monthly, 12*month) || !models["gpt-4o"].Priced || models["mystery-model"].Priced || !near(models["claude-sonnet-4-5"].Monthly, 21*month) {
		t.Errorf("models = %+v", models)
	}

	// llm costs 1.68/h: llama has 4M of the 8M tokens, so half of 1.68×168 = 141.12 for 4M tokens.
	if len(got.SelfHosted) != 2 {
		t.Fatalf("self-hosted = %+v", got.SelfHosted)
	}
	for _, m := range got.SelfHosted {
		if m.Namespace != "llm" || !near(m.MonthlyCost, 1.68*0.5*730) || m.CostPer1M == nil || !near(*m.CostPer1M, 141.12/4) {
			t.Errorf("%s = %+v (cost per 1M %v)", m.Model, m, m.CostPer1M)
		}
	}
	if !near(got.SelfHosted[0].TokensPerHour, 4e6/168) {
		t.Errorf("tokens per hour = %v", got.SelfHosted[0].TokensPerHour)
	}

	empty := BuildAICosts(metrics.LLMUsage{}, snap, nil)
	if empty.API.Monthly != 0 || len(empty.API.Teams) != 0 || empty.API.Teams == nil || empty.SelfHosted == nil || len(empty.API.Sources) != 0 {
		t.Errorf("no usage = %+v: lists must be empty, not null", empty)
	}
}
