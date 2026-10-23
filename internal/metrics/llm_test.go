package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func TestLLMUsage(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		s := func(v string, kv ...string) map[string]any {
			m := map[string]string{}
			for i := 0; i < len(kv); i += 2 {
				m[kv[i]] = kv[i+1]
			}
			return map[string]any{"metric": m, "value": []any{1700000000, v}}
		}
		var result []map[string]any
		switch {
		case strings.HasPrefix(q, "count("):
			result = []map[string]any{s("3")} // collected through an OpenTelemetry collector
		case strings.Contains(q, "litellm_spend_metric_total"):
			result = []map[string]any{s("12.5", "team_alias", "search", "team", "t-1", "model", "gpt-4o"), s("1", "team", "t-2", "model", "gpt-4o-mini")}
		case strings.Contains(q, "litellm_input_tokens_metric_total"):
			result = []map[string]any{s("1000000", "team_alias", "search", "team", "t-1", "model", "gpt-4o")}
		case strings.Contains(q, "litellm_output_tokens_metric_total"):
			result = []map[string]any{s("250000", "team_alias", "search", "team", "t-1", "model", "gpt-4o")}
		case strings.Contains(q, "gen_ai_client_token_usage_sum"):
			result = []map[string]any{
				s("2000000", "k8s_namespace_name", "shop", "gen_ai_request_model", "claude-sonnet-4-5", "gen_ai_token_type", "input"),
				s("400000", "k8s_namespace_name", "shop", "gen_ai_request_model", "claude-sonnet-4-5", "gen_ai_token_type", "output"),
				s("7", "k8s_namespace_name", "shop", "gen_ai_request_model", "claude-sonnet-4-5", "gen_ai_token_type", "embedding"),
			}
		case strings.Contains(q, "gen_ai_client_inference_usage_input_tokens_total"):
			result = []map[string]any{s("500000", "k8s_namespace_name", "shop", "gen_ai_request_model", "claude-sonnet-4-5")}
		case strings.Contains(q, "gen_ai_client_inference_usage_output_tokens_total"):
		case strings.Contains(q, "vllm:prompt_tokens_total"):
			result = []map[string]any{s("3000000", "namespace", "llm", "model_name", "llama-3-8b")}
		case strings.Contains(q, "vllm:generation_tokens_total"):
			result = []map[string]any{s("1000000", "namespace", "llm", "model_name", "llama-3-8b")}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
	}))
	defer srv.Close()
	c, err := NewVMClient(VMOptions{Endpoint: srv.URL, LabelSelector: `cluster="dev"`})
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.LLMUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(u.Proxy, func(i, j int) bool { return u.Proxy[i].Team < u.Proxy[j].Team })
	if len(u.Proxy) != 2 || u.Proxy[0] != (ProxyUsage{Team: "search", Model: "gpt-4o", Spend: 12.5, Input: 1e6, Output: 250000}) || u.Proxy[1].Team != "t-2" {
		t.Errorf("proxy = %+v: team_alias names the team, team is the fallback", u.Proxy)
	}
	// The histogram's input and output, plus the new input counter; other token types are not counted.
	if len(u.Clients) != 1 || u.Clients[0] != (TokenUsage{Namespace: "shop", Model: "claude-sonnet-4-5", Input: 2.5e6, Output: 400000}) {
		t.Errorf("clients = %+v", u.Clients)
	}
	if len(u.Served) != 1 || u.Served[0] != (TokenUsage{Namespace: "llm", Model: "llama-3-8b", Input: 3e6, Output: 1e6}) {
		t.Errorf("served = %+v", u.Served)
	}
	all := strings.Join(queries, "\n")
	for _, want := range []string{
		`sum by (team_alias, team, model) (increase(litellm_spend_metric_total{cluster="dev"}[7d]))`,
		`sum by (k8s_namespace_name, gen_ai_request_model, gen_ai_token_type) (increase(gen_ai_client_token_usage_sum{cluster="dev"}[7d]))`,
		`sum by (namespace, model_name) (increase(vllm:generation_tokens_total{cluster="dev"}[7d]))`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no query %s in:\n%s", want, all)
		}
	}
}
