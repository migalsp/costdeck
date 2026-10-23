package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/migalsp/costdeck-operator/internal/config"
)

func TestLLMModelPricesInSettings(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	seedCalendars(t, server)
	ok := `{"pricing":{"llmModels":[{"model":" claude-sonnet-* ","inputPer1M":"3","outputPer1M":"15"},{"model":"gpt-4o","inputPer1M":"2.50","outputPer1M":"10"}]}}`
	if rr := send(t, server, "PUT", "/api/settings", ok); rr.Code != http.StatusOK {
		t.Fatalf("valid prices: %d %s", rr.Code, rr.Body.String())
	}
	cfg, err := config.Get(context.Background(), server.Client)
	if err != nil || len(cfg.Spec.Pricing.LLMModels) != 2 || cfg.Spec.Pricing.LLMModels[0].Model != "claude-sonnet-*" {
		t.Fatalf("saved = %+v (%v)", cfg.Spec.Pricing.LLMModels, err)
	}
	var got struct {
		Pricing PricingSettingsResponse `json:"pricing"`
	}
	_ = json.Unmarshal(send(t, server, "GET", "/api/settings", "").Body.Bytes(), &got)
	if len(got.Pricing.LLMModels) != 2 || got.Pricing.LLMModels[1].InputPer1M != "2.50" {
		t.Errorf("settings return %+v", got.Pricing.LLMModels)
	}
	for name, body := range map[string]string{
		"an empty model":  `{"pricing":{"llmModels":[{"model":"","inputPer1M":"1","outputPer1M":"1"}]}}`,
		"a broken glob":   `{"pricing":{"llmModels":[{"model":"gpt-[","inputPer1M":"1","outputPer1M":"1"}]}}`,
		"a model twice":   `{"pricing":{"llmModels":[{"model":"a","inputPer1M":"1","outputPer1M":"1"},{"model":"a","inputPer1M":"2","outputPer1M":"2"}]}}`,
		"a word as price": `{"pricing":{"llmModels":[{"model":"a","inputPer1M":"cheap","outputPer1M":"1"}]}}`,
	} {
		if rr := send(t, server, "PUT", "/api/settings", body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rr.Code, rr.Body.String())
		}
	}
	if rr := send(t, server, "PUT", "/api/settings", `{"pricing":{"llmModels":[]}}`); rr.Code != http.StatusOK {
		t.Fatalf("clearing: %d", rr.Code)
	}
	if cfg, _ := config.Get(context.Background(), server.Client); len(cfg.Spec.Pricing.LLMModels) != 0 {
		t.Errorf("prices not cleared: %+v", cfg.Spec.Pricing.LLMModels)
	}
}

func TestAICostsWithoutVictoriaMetrics(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	rr := send(t, server, "GET", "/api/finops/ai", "")
	var got aiCostsResponse
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &got) != nil {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	if got.Available || got.API.Teams == nil || got.SelfHosted == nil || got.WindowDays != 7 {
		t.Errorf("without VictoriaMetrics = %+v", got)
	}
	out, err := server.toolAICosts(context.Background())
	if err != nil || !strings.Contains(out, "VictoriaMetrics is not connected") {
		t.Errorf("tool = %s (%v)", out, err)
	}
}
