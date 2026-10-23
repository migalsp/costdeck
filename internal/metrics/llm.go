package metrics

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// LLM token counts come from three kinds of exporters, all scraped into VictoriaMetrics:
//   - the LiteLLM proxy, which also reports what the tokens cost (litellm_spend_metric),
//     per LiteLLM team and model;
//   - applications instrumented with OpenTelemetry GenAI metrics: the released
//     gen_ai.client.token.usage histogram (input and output in gen_ai_token_type) and the
//     counters that replace it (gen_ai.client.inference.usage.input_tokens and
//     output_tokens);
//   - vLLM, the server of self-hosted models (vllm:prompt_tokens and
//     vllm:generation_tokens per model_name).

// LLMWindow is how far back token counts are summed; monthly figures extrapolate it.
const LLMWindow = 7 * 24 * time.Hour

const llmCacheTTL = 2 * time.Minute

// LLMUsage is what the cluster's LLM traffic did over LLMWindow.
type LLMUsage struct {
	// Proxy rows come from LiteLLM, with the spend it worked out.
	Proxy []ProxyUsage
	// Clients are tokens OpenTelemetry-instrumented applications counted, per namespace.
	Clients []TokenUsage
	// Served are tokens vLLM generated for self-hosted models, per namespace.
	Served []TokenUsage
}

// ProxyUsage is one LiteLLM team and model.
type ProxyUsage struct {
	Team   string
	Model  string
	Spend  float64
	Input  float64
	Output float64
}

// TokenUsage is one namespace and model.
type TokenUsage struct {
	Namespace string
	Model     string
	Input     float64
	Output    float64
}

// LLMUsage sums the LLM tokens of the last LLMWindow.
func (c *VMClient) LLMUsage(ctx context.Context) (LLMUsage, error) {
	var out LLMUsage
	sel := ""
	if c.labelSelector != "" {
		sel = c.labelSelector
	}
	inc := func(metric, by string) string {
		return fmt.Sprintf("sum by (%s) (increase(%s{%s}[%s]))", by, metric, sel, promDuration(LLMWindow))
	}

	// LiteLLM: team_alias names the team when set, team is its ID.
	proxy := map[[2]string]*ProxyUsage{}
	for _, m := range []struct {
		metric string
		set    func(*ProxyUsage, float64)
	}{
		{"litellm_spend_metric_total", func(u *ProxyUsage, v float64) { u.Spend += v }},
		{"litellm_input_tokens_metric_total", func(u *ProxyUsage, v float64) { u.Input += v }},
		{"litellm_output_tokens_metric_total", func(u *ProxyUsage, v float64) { u.Output += v }},
	} {
		rows, err := c.rows(ctx, inc(m.metric, "team_alias, team, model"))
		if err != nil {
			return out, fmt.Errorf("query %s: %w", m.metric, err)
		}
		for _, r := range rows {
			team := firstOf(r.labels["team_alias"], r.labels["team"])
			key := [2]string{team, r.labels["model"]}
			if proxy[key] == nil {
				proxy[key] = &ProxyUsage{Team: team, Model: r.labels["model"]}
			}
			m.set(proxy[key], r.value)
		}
	}
	for _, u := range proxy {
		out.Proxy = append(out.Proxy, *u)
	}

	// OpenTelemetry: through a collector the scrape's namespace is the collector's, and the
	// application's is the k8s.namespace.name resource attribute.
	nsLabel := "namespace"
	if probe, err := c.rows(ctx, `count({__name__=~"gen_ai_client_token_usage_sum|gen_ai_client_inference_usage_input_tokens_total",k8s_namespace_name!=""`+andSel(sel)+`})`); err == nil && len(probe) > 0 {
		nsLabel = "k8s_namespace_name"
	}
	clients := map[[2]string]*TokenUsage{}
	add := func(dst map[[2]string]*TokenUsage, ns, model string, input, output float64) {
		key := [2]string{ns, model}
		if dst[key] == nil {
			dst[key] = &TokenUsage{Namespace: ns, Model: model}
		}
		dst[key].Input += input
		dst[key].Output += output
	}
	hist, err := c.rows(ctx, inc("gen_ai_client_token_usage_sum", nsLabel+", gen_ai_request_model, gen_ai_token_type"))
	if err != nil {
		return out, fmt.Errorf("query gen_ai_client_token_usage: %w", err)
	}
	for _, r := range hist {
		in, outTokens := 0.0, 0.0
		switch r.labels["gen_ai_token_type"] {
		case "input":
			in = r.value
		case "output":
			outTokens = r.value
		default:
			continue
		}
		add(clients, r.labels[nsLabel], r.labels["gen_ai_request_model"], in, outTokens)
	}
	for _, m := range []struct {
		metric string
		input  bool
	}{{"gen_ai_client_inference_usage_input_tokens_total", true}, {"gen_ai_client_inference_usage_output_tokens_total", false}} {
		rows, err := c.rows(ctx, inc(m.metric, nsLabel+", gen_ai_request_model"))
		if err != nil {
			return out, fmt.Errorf("query %s: %w", m.metric, err)
		}
		for _, r := range rows {
			if m.input {
				add(clients, r.labels[nsLabel], r.labels["gen_ai_request_model"], r.value, 0)
			} else {
				add(clients, r.labels[nsLabel], r.labels["gen_ai_request_model"], 0, r.value)
			}
		}
	}
	for _, u := range clients {
		out.Clients = append(out.Clients, *u)
	}

	served := map[[2]string]*TokenUsage{}
	for _, m := range []struct {
		metric string
		input  bool
	}{{"vllm:prompt_tokens_total", true}, {"vllm:generation_tokens_total", false}} {
		rows, err := c.rows(ctx, inc(m.metric, "namespace, model_name"))
		if err != nil {
			return out, fmt.Errorf("query %s: %w", m.metric, err)
		}
		for _, r := range rows {
			if m.input {
				add(served, r.labels["namespace"], r.labels["model_name"], r.value, 0)
			} else {
				add(served, r.labels["namespace"], r.labels["model_name"], 0, r.value)
			}
		}
	}
	for _, u := range served {
		out.Served = append(out.Served, *u)
	}
	return out, nil
}

// llmCache shares the LLM token counts for a few minutes.
type llmCache struct {
	mu    sync.Mutex
	key   string
	at    time.Time
	usage LLMUsage
}

type labelledValue struct {
	labels map[string]string
	value  float64
}

// rows runs an instant query and returns every series with its labels.
func (c *VMClient) rows(ctx context.Context, query string) ([]labelledValue, error) {
	resp, err := c.query(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make([]labelledValue, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		v, err := sampleValue(r.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, labelledValue{labels: r.Metric, value: v})
	}
	return out, nil
}

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func andSel(sel string) string {
	if sel == "" {
		return ""
	}
	return "," + sel
}

// LLMUsage returns the cluster's LLM token counts from VictoriaMetrics, shared for a few
// minutes, or ErrNoHistory when VictoriaMetrics is not enabled.
func (p *Provider) LLMUsage(ctx context.Context) (LLMUsage, error) {
	vm, _, err := p.resolve(ctx)
	if vm == nil {
		if err == nil {
			err = ErrNoHistory
		}
		return LLMUsage{}, err
	}
	p.llm.mu.Lock()
	defer p.llm.mu.Unlock()
	key := vm.Endpoint() + "|" + vm.labelSelector
	if p.llm.key == key && time.Since(p.llm.at) < llmCacheTTL {
		return p.llm.usage, nil
	}
	usage, err := vm.LLMUsage(ctx)
	if err != nil {
		return LLMUsage{}, err
	}
	p.llm.key, p.llm.at, p.llm.usage = key, time.Now(), usage
	return usage, nil
}
