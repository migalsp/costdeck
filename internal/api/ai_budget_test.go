package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/ai"
)

// fakeModel is an OpenAI-compatible endpoint that reports 120 input and 7 output tokens
// for every answer.
func fakeModel(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"shop costs the most.\"},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":7}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func assistantServer(t *testing.T, budget int64, baseURL string) *Server {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	cfg := &finopsv1.CostDeckConfig{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"}, Spec: finopsv1.CostDeckConfigSpec{
		Integrations: finopsv1.IntegrationsConfig{AI: &finopsv1.AIIntegrationConfig{Enabled: true, Provider: "local", Model: "fake-model", BaseURL: baseURL + "/v1", MonthlyTokenBudget: budget}},
		Pricing:      finopsv1.PricingConfig{LLMModels: []finopsv1.LLMModelPrice{{Model: "fake-*", InputPer1M: "10", OutputPer1M: "100"}}},
	}}
	if err := server.Client.Create(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return server
}

const question = `{"messages":[{"role":"user","content":"Which namespace costs the most?"}]}`

func TestAssistantCountsTokensAgainstItsBudget(t *testing.T) {
	model, calls := fakeModel(t)
	server := assistantServer(t, 200, model.URL)

	rr := send(t, server, "POST", "/api/ai/chat", question)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "shop costs the most.") {
		t.Fatalf("first answer: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `"usage"`) {
		t.Errorf("usage events reached the browser: %s", rr.Body.String())
	}
	u, err := server.aiUsage(context.Background(), time.Now())
	if err != nil || u.InputTokens != 120 || u.OutputTokens != 7 || u.Requests != 1 || u.Estimated {
		t.Fatalf("usage after one answer = %+v (%v)", u, err)
	}

	// Under the budget (127 of 200) the next question is answered and counted too.
	if rr := send(t, server, "POST", "/api/ai/chat", question); rr.Code != http.StatusOK {
		t.Fatalf("second answer: %d", rr.Code)
	}
	// 254 of 200: the assistant refuses, without asking the model.
	before := calls.Load()
	rr = send(t, server, "POST", "/api/ai/chat", question)
	if rr.Code != http.StatusTooManyRequests || !strings.Contains(rr.Body.String(), "used its budget of 200 tokens") {
		t.Errorf("past the budget: %d %s", rr.Code, rr.Body.String())
	}
	if rr := send(t, server, "POST", "/api/ai/report/generate", `{}`); rr.Code != http.StatusTooManyRequests {
		t.Errorf("AI reports ignore the budget: %d", rr.Code)
	}
	if calls.Load() != before {
		t.Error("the model was asked although the budget was spent")
	}

	// Settings show the month's usage, priced at the model's price, and lift the cap.
	var settings struct {
		Integrations struct {
			AI AISettingsResponse `json:"ai"`
		} `json:"integrations"`
	}
	_ = json.Unmarshal(send(t, server, "GET", "/api/settings", "").Body.Bytes(), &settings)
	got := settings.Integrations.AI
	if got.MonthlyTokenBudget != 200 || got.Usage.Tokens() != 254 || got.UsageCost == nil || *got.UsageCost != (240*10+14*100)/1e6 {
		t.Errorf("settings = %+v (cost %v)", got, got.UsageCost)
	}
	costs := aiCostsOf(t, server)
	if costs.Assistant == nil || costs.Assistant.Tokens() != 254 || costs.Assistant.Model != "fake-model" {
		t.Errorf("AI costs assistant = %+v", costs.Assistant)
	}
	if rr := send(t, server, "PUT", "/api/settings", `{"integrations":{"ai":{"baseUrl":"`+model.URL+`/v1","monthlyTokenBudget":0}}}`); rr.Code != http.StatusOK {
		t.Fatalf("removing the budget: %d %s", rr.Code, rr.Body.String())
	}
	if rr := send(t, server, "POST", "/api/ai/chat", question); rr.Code != http.StatusOK {
		t.Errorf("without a budget: %d", rr.Code)
	}
	if rr := send(t, server, "PUT", "/api/settings", `{"integrations":{"ai":{"baseUrl":"`+model.URL+`/v1","monthlyTokenBudget":-1}}}`); rr.Code != http.StatusBadRequest {
		t.Errorf("a negative budget: %d", rr.Code)
	}
}

func aiCostsOf(t *testing.T, server *Server) aiCostsResponse {
	t.Helper()
	var got aiCostsResponse
	_ = json.Unmarshal(send(t, server, "GET", "/api/finops/ai", "").Body.Bytes(), &got)
	return got
}

// Usage is kept per month, and only a year of months.
func TestAIUsageMonths(t *testing.T) {
	model, _ := fakeModel(t)
	server := assistantServer(t, 0, model.URL)
	ctx := context.Background()
	start := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	for i := range 14 {
		if err := server.addAIUsage(ctx, start.AddDate(0, i, 0), aiUsageFor(10, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := server.addAIUsage(ctx, start.AddDate(0, 13, 0), aiUsageFor(5, 5)); err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	if err := server.Client.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: aiUsageConfigMap}, &cm); err != nil {
		t.Fatal(err)
	}
	if len(cm.Data) != aiUsageMonths || cm.Data["2025-01"] != "" || cm.Data["2025-02"] != "" {
		t.Errorf("months kept = %d: %v", len(cm.Data), cm.Data)
	}
	u, _ := server.aiUsage(ctx, start.AddDate(0, 13, 0))
	if u.Month != "2026-02" || u.InputTokens != 15 || u.OutputTokens != 6 || u.Requests != 2 {
		t.Errorf("February 2026 = %+v", u)
	}
	if u, _ := server.aiUsage(ctx, start.AddDate(0, 20, 0)); u.Tokens() != 0 || u.Month != "2026-09" {
		t.Errorf("a month without usage = %+v", u)
	}
}

func aiUsageFor(in, out int64) ai.Usage { return ai.Usage{InputTokens: in, OutputTokens: out} }
