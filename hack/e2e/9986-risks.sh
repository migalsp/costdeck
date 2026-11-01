# shellcheck shell=bash

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it changes the VictoriaMetrics settings of the installed release"
  return 0
fi

risk_workload() {
  kubectl apply -f - >/dev/null <<YAML || fail "applying $1"
apiVersion: apps/v1
kind: Deployment
metadata: {name: $1, namespace: smoke-risks}
spec:
  replicas: 1
  selector: {matchLabels: {app: $1}}
  template:
    metadata: {labels: {app: $1}}
    spec:
      terminationGracePeriodSeconds: 1
      containers:
        - name: main
          image: $BUSYBOX_IMAGE
          command: [sh, -c, '$2']
          resources: $3
YAML
}
risk_pod() { kubectl get pods -n smoke-risks -l "app=$1" -o jsonpath='{.items[0].metadata.name}'; }
risk_status() { kubectl get pods -n smoke-risks -l "app=$1" -o jsonpath="{.items[0].status.containerStatuses[0].$2}"; }
risk_last_termination_is() { [[ "$(risk_status "$1" lastState.terminated.reason)" == "$2" ]]; }
risk_waiting_is() { [[ "$(risk_status "$1" state.waiting.reason)" == "$2" ]]; }
risk_hpa_limited() { [[ "$(kubectl get hpa risk-hpa -n smoke-risks -o jsonpath='{.status.conditions[?(@.type=="ScalingLimited")].reason}')" == TooManyReplicas ]]; }

log "Workloads in trouble: an OOM kill, a crash loop and an HPA at its maximum, from real pods"
start_victoriametrics 7
new_namespace smoke-risks
risk_workload risk-oom 'exec dd if=/dev/zero of=/dev/null bs=48M count=1' '{requests: {cpu: 10m, memory: 16Mi}, limits: {memory: 16Mi}}'
risk_workload risk-crash 'echo starting; exit 3' '{requests: {cpu: 10m, memory: 16Mi}}'
risk_workload risk-hpa 'while :; do :; done' '{requests: {cpu: 10m, memory: 16Mi}, limits: {cpu: 100m}}'
risk_workload risk-throttled 'sleep 3600' '{requests: {cpu: 200m, memory: 64Mi}, limits: {cpu: 200m, memory: 256Mi}}'
risk_workload risk-near 'sleep 3600' '{requests: {cpu: 10m, memory: 256Mi}, limits: {memory: 256Mi}}'
risk_workload risk-evicted 'sleep 3600' '{requests: {cpu: 10m, memory: 64Mi}}'
kubectl apply -f - >/dev/null <<'YAML' || fail "applying the HorizontalPodAutoscaler"
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: {name: risk-hpa, namespace: smoke-risks}
spec:
  scaleTargetRef: {apiVersion: apps/v1, kind: Deployment, name: risk-hpa}
  minReplicas: 1
  maxReplicas: 1
  metrics: [{type: Resource, resource: {name: cpu, target: {type: Utilization, averageUtilization: 50}}}]
YAML
for w in risk-throttled risk-near; do
  kubectl rollout status "deploy/$w" -n smoke-risks --timeout=120s >/dev/null || fail "$w did not start"
done
wait_for "risk-oom is OOM-killed" risk_last_termination_is risk-oom OOMKilled
wait_for "risk-crash is crash-looping" risk_waiting_is risk-crash CrashLoopBackOff
TIMEOUT=240 wait_for "the HorizontalPodAutoscaler of risk-hpa wants more than its maximum" risk_hpa_limited

python3 - "$(risk_pod risk-throttled)" "$(risk_pod risk-near)" <<'PY' | curl -sf --data-binary @- "localhost:$VM_PORT/api/v1/import" || fail "importing the synthetic pressure history"
import json, sys, time
throttled, near = sys.argv[1], sys.argv[2]
now = int(time.time()) // 60 * 60
start, step, mi = now - 2 * 86400, 60, 2**20
def series(labels, fn):
    ts = list(range(start, now + 1, step))
    print(json.dumps({"metric": labels, "values": [fn(t) for t in ts], "timestamps": [t * 1000 for t in ts]}))
def container(pod):
    return {"namespace": "smoke-risks", "pod": pod, "container": "main"}
series({"__name__": "container_cpu_cfs_periods_total", **container(throttled)}, lambda t: (t - start) * 10)
series({"__name__": "container_cpu_cfs_throttled_periods_total", **container(throttled)}, lambda t: (t - start) * 3)
series({"__name__": "container_cpu_usage_seconds_total", **container(throttled)}, lambda t: round((t - start) * 0.02, 3))
series({"__name__": "container_memory_working_set_bytes", **container(throttled)}, lambda t: 40 * mi)
series({"__name__": "container_cpu_usage_seconds_total", **container(near)}, lambda t: round((t - start) * 0.005, 3))
series({"__name__": "container_memory_working_set_bytes", **container(near)}, lambda t: 240 * mi)
series({"__name__": "kube_pod_status_reason", "namespace": "smoke-risks", "pod": "risk-evicted-5c6b7d8f9-bcdfg", "reason": "Evicted"},
       lambda t: 1 if now - t < 86400 else 0)
PY
curl -sf "localhost:$VM_PORT/internal/force_flush" >/dev/null
curl -sf "localhost:$VM_PORT/internal/resetRollupResultCache" >/dev/null
ok "imported CPU throttling, memory close to a limit and an eviction"
wait_for "VictoriaMetrics serves the pressure history" vm_series_at_least '{namespace="smoke-risks"}' 7

log "The risks name each problem, its fix and what the fix costs"
RISKS_REPORT=$(mktemp)
risks_ready() {
  api GET /api/namespaces/smoke-risks/risks >"$RISKS_REPORT" &&
    jq -e '[.risks[].code] | index("evicted") != null' "$RISKS_REPORT" >/dev/null
}
TIMEOUT=330 wait_for "risks from the pods, the HorizontalPodAutoscaler and VictoriaMetrics, within the five minutes risks are cached" risks_ready
risk_expect() {
  local found
  found=$(jq -c --arg w "$2" --arg c "$3" '[.risks[] | select(.workload == $w and .code == $c)][0]' "$RISKS_REPORT")
  echo "$found" | jq -e "$4" >/dev/null || fail "$1: $found"
  ok "$1"
}
risk_expect "an OOM-killed container is critical, and more memory costs money" risk-oom oom-killed \
  '.severity == "critical" and .fixMonthlyCost > 0'
risk_expect "a crash loop names its exit code and how to read the log" risk-crash restarts \
  '.severity == "critical" and (.message | test("exit code 3")) and (.fix | test("kubectl logs -n smoke-risks deployment/risk-crash -c main --previous"))'
risk_expect "an HPA held at its maximum says how many replicas it wants" risk-hpa hpa-at-max \
  '(.fix | test("maxReplicas of risk-hpa to [0-9]+")) and .fixMonthlyCost > 0'
risk_expect "a CPU limit that throttles 30% of the time is critical, and raising it is free" risk-throttled cpu-throttled \
  '.severity == "critical" and (.message | test("30%")) and .fixMonthlyCost == 0'
risk_expect "memory at 94% of its limit is a warning" risk-near memory-near-limit '.severity == "warning" and (.message | test("94%"))'
risk_expect "an eviction in the last day of history is critical" risk-evicted evicted '.severity == "critical"'
jq -e '.historical and .window == "7d" and .risks[0].severity == "critical"' "$RISKS_REPORT" >/dev/null ||
  fail "the report is not historical or not sorted: $(jq -c '{historical, window, first: .risks[0].severity}' "$RISKS_REPORT")"
ok "critical risks first, over the configured seven days"

log "Right-sizing does not cut what is under pressure"
api GET /api/namespaces/smoke-risks/recommendations |
  jq -e '.workloads[] | select(.name == "risk-throttled") | .containers[0].cpu | .action == "keep" and (.held | test("throttled in 30%"))' >/dev/null ||
  fail "the CPU request of a throttled workload is still cut: $(api GET /api/namespaces/smoke-risks/recommendations | jq -c '.workloads[] | select(.name == "risk-throttled") | .containers[0].cpu')"
ok "the throttled workload keeps its CPU request"
rm -f "$RISKS_REPORT"

log "Alerts about new critical risks are switched on apart from the anomaly alerts"
RISK_ANOMALIES=$(api GET /api/budgets | jq -c '.anomalies')
track_call PUT /api/settings '{"alerts":{"performanceRisks":{"enabled":false}}}'
expect_code 200 "switching performance risk alerts on" PUT /api/settings '{"alerts":{"performanceRisks":{"enabled":true}}}' -b "$JAR"
api GET /api/budgets | jq -e --argjson anomalies "$RISK_ANOMALIES" '.performanceRisks.enabled == true and .anomalies == $anomalies' >/dev/null ||
  fail "the setting did not stick or touched the anomaly alerts: $(api GET /api/budgets | jq -c '{performanceRisks, anomalies}')"
ok "on, and the anomaly alerts are as they were"
api PUT /api/settings '{"integrations":{"victoriaMetrics":{"enabled":false}}}' >/dev/null
