# shellcheck shell=bash
# AI costs from a real VictoriaMetrics fed synthetic counters: LiteLLM's spend for a
# LiteLLM team, OpenTelemetry GenAI tokens of an application priced at a model price set
# through the API, and vLLM tokens divided into the cost of the namespace serving them.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it changes the VictoriaMetrics settings and the model prices of the installed release"
  return 0
fi

ai() { api GET /api/finops/ai; }
team_monthly() { ai | jq -r --arg t "$1" '.api.teams[] | select(.team == $t) | .monthly'; }
unpriced_has() { ai | jq -e --arg m "$1" '.api.unpriced | index($m) != null' >/dev/null; }
# A month is 730/168 of the seven days the counts cover; amounts are compared to a cent.
close_to() { awk -v a="$1" -v b="$2" 'BEGIN { exit !(a != "" && (a - b) < 0.01 && (b - a) < 0.01) }'; }
team_costs() { close_to "$(team_monthly "$1")" "$2"; }

log "Token counters from LiteLLM, an instrumented application and vLLM"
new_namespace smoke-ai-app
kubectl label namespace smoke-ai-app team=smoke-payments --overwrite >/dev/null
new_namespace smoke-ai-llm
deployment smoke-ai-llm vllm 1
start_victoriametrics 7
python3 - <<'PY' | curl -sf --data-binary @- "localhost:$VM_PORT/api/v1/import" || fail "importing the token counters"
import json, time
# The last sample stays clear of VictoriaMetrics' search latency offset (30 s), so the
# first query, whose answer is cached for minutes, already sees the whole increase.
now = int(time.time()) // 60 * 60 - 120
ts = list(range(now - 7200, now + 1, 120))
def counter(labels, total):
    values = [round(total * i / (len(ts) - 1), 3) for i in range(len(ts))]
    print(json.dumps({"metric": labels, "values": values, "timestamps": [t * 1000 for t in ts]}))
team = {"team_alias": "smoke-search", "team": "t-smoke", "model": "gpt-4o"}
counter({"__name__": "litellm_spend_metric_total", **team}, 14)
counter({"__name__": "litellm_input_tokens_metric_total", **team}, 1000000)
counter({"__name__": "litellm_output_tokens_metric_total", **team}, 200000)
app = {"namespace": "smoke-ai-app", "gen_ai_request_model": "claude-sonnet-4-5", "gen_ai_provider_name": "anthropic"}
counter({"__name__": "gen_ai_client_token_usage_sum", **app, "gen_ai_token_type": "input"}, 2000000)
counter({"__name__": "gen_ai_client_token_usage_sum", **app, "gen_ai_token_type": "output"}, 400000)
served = {"namespace": "smoke-ai-llm", "model_name": "smoke-llama"}
counter({"__name__": "vllm:prompt_tokens_total", **served}, 3000000)
counter({"__name__": "vllm:generation_tokens_total", **served}, 1000000)
PY
curl -sf "localhost:$VM_PORT/internal/force_flush" >/dev/null
# Token counts are cached for two minutes.
TIMEOUT=150 wait_for "LiteLLM's spend is charged to its team" team_costs smoke-search "$(awk 'BEGIN { print 14 * 730 / 168 }')"
unpriced_has claude-sonnet-4-5 || fail "the application's model should be unpriced: $(ai | jq -c .api.unpriced)"
ok "the application's model has no price yet"

log "A model price set through the API prices the application's tokens"
track_call PUT /api/settings '{"pricing":{"llmModels":[]}}'
api PUT /api/settings '{"pricing":{"llmModels":[{"model":"claude-sonnet-*","inputPer1M":"3","outputPer1M":"15"}]}}' >/dev/null || fail "setting a model price"
# 2M input at 3 and 0.4M output at 15 a million: 12 over the seven days.
wait_for "the tokens are charged to the namespace's team" team_costs smoke-payments "$(awk 'BEGIN { print 12 * 730 / 168 }')"
data=$(ai)
echo "$data" | jq -e '.api.teams[] | select(.team == "smoke-payments") | .source == "opentelemetry" and .namespaces == ["smoke-ai-app"]' >/dev/null ||
  fail "the team row is wrong: $(echo "$data" | jq -c '.api.teams')"
echo "$data" | jq -e '.api.unpriced | index("claude-sonnet-4-5") == null' >/dev/null || fail "the model is still unpriced"
ok "OpenTelemetry tokens priced and charged to smoke-payments"

log "A self-hosted model costs its namespace's run rate per token served"
self_hosted_priced() { ai | jq -e '.selfHosted[] | select(.model == "smoke-llama" and .namespace == "smoke-ai-llm") | .costPer1M > 0 and .tokensPerHour > 0' >/dev/null; }
# The namespace's cost needs its first usage sample, taken within a minute or two.
TIMEOUT=150 wait_for "smoke-llama has a cost per million tokens" self_hosted_priced
