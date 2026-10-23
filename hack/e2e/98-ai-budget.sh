# shellcheck shell=bash
# The assistant counts the tokens every answer uses against a monthly budget and refuses
# new questions once it is spent. A busybox CGI script stands in for an OpenAI-compatible
# model server; it reports 127 tokens per answer.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it changes the AI settings of the installed release"
  return 0
fi

FAKE_URL="http://fakeai.smoke-ai-budget.svc:8080/cgi-bin"
AI_NO_BUDGET='{"integrations":{"ai":{"enabled":true,"provider":"local","model":"fake","baseUrl":"'"$FAKE_URL"'","monthlyTokenBudget":0}}}'
CHAT='{"messages":[{"role":"user","content":"Which namespace costs the most?"}]}'
used_tokens() { api GET /api/settings | jq -r '.integrations.ai.usage | .inputTokens + .outputTokens'; }
chat() { curl -sS -N -b "$JAR" -H 'Content-Type: application/json' -d "$CHAT" "localhost:$API_PORT/api/ai/chat"; }

log "A fake model server and a budget with room for 200 more tokens"
new_namespace smoke-ai-budget
kubectl apply -f - >/dev/null <<'YAML' || fail "applying the fake model server"
apiVersion: v1
kind: ConfigMap
metadata: {name: fakeai, namespace: smoke-ai-budget}
data:
  chat: |
    #!/bin/sh
    cat >/dev/null
    printf 'Content-Type: text/event-stream\r\n\r\n'
    printf 'data: {"choices":[{"delta":{"content":"smoke-history costs the most."},"finish_reason":"stop"}]}\n\n'
    printf 'data: {"choices":[],"usage":{"prompt_tokens":120,"completion_tokens":7}}\n\n'
    printf 'data: [DONE]\n\n'
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: fakeai, namespace: smoke-ai-budget}
spec:
  replicas: 1
  selector: {matchLabels: {app: fakeai}}
  template:
    metadata: {labels: {app: fakeai}}
    spec:
      containers:
        - name: httpd
          image: registry.k8s.io/e2e-test-images/busybox:1.36.1-1
          command: [httpd, -f, -p, "8080", -h, /www]
          volumeMounts: [{name: cgi, mountPath: /www/cgi-bin}]
          readinessProbe: {tcpSocket: {port: 8080}, periodSeconds: 2}
      volumes: [{name: cgi, configMap: {name: fakeai, defaultMode: 0755}}]
---
apiVersion: v1
kind: Service
metadata: {name: fakeai, namespace: smoke-ai-budget}
spec: {selector: {app: fakeai}, ports: [{port: 8080}]}
YAML
kubectl rollout status deploy/fakeai -n smoke-ai-budget --timeout=120s >/dev/null || fail "the fake model server did not start"
track_call PUT /api/settings '{"integrations":{"ai":{"enabled":false,"monthlyTokenBudget":0}}}'
api PUT /api/settings "$AI_NO_BUDGET" >/dev/null || fail "enabling the assistant"
# Usage is kept for the month across runs: the budget leaves room for 200 more tokens.
before=$(used_tokens)
AI_ON='{"integrations":{"ai":{"enabled":true,"provider":"local","model":"fake","baseUrl":"'"$FAKE_URL"'","monthlyTokenBudget":'"$((before + 200))"'}}}'
api PUT /api/settings "$AI_ON" >/dev/null || fail "setting the budget"

log "Each answer is counted; the budget refuses questions once it is spent"
# The Service may take a moment to route to the new pod; refused attempts use no tokens.
answer=""
answered() { answer=$(chat) && echo "$answer" | grep -q 'smoke-history costs the most.'; }
wait_for "the assistant answers through the fake model server" answered
echo "$answer" | grep -q '"usage"' && fail "usage events reached the browser: $answer"
[[ "$(used_tokens)" == "$((before + 127))" ]] || fail "the answer was not counted: $(api GET /api/settings | jq -c .integrations.ai.usage)"
ok "the answer used 127 tokens, counted for the month"
chat >/dev/null
code=$(api_code POST /api/ai/chat "$CHAT" -b "$JAR")
[[ "$code" == 429 ]] || fail "a question past the budget answered $code, want 429"
ok "past the budget the assistant refuses with 429"
# The settings API takes the AI section whole: baseUrl would be cleared without it.
api PUT /api/settings "$AI_NO_BUDGET" >/dev/null || fail "removing the budget"
chat | grep -q 'smoke-history costs the most.' || fail "without a budget the assistant should answer again"
ok "without a budget it answers again"
