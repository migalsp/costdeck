# shellcheck shell=bash
# Usage history from a real VictoriaMetrics, fed eight days of synthetic data: a namespace
# that runs 14:00-02:00 UTC and is scaled to zero otherwise, plus a finished Job pod whose
# large requests must not count.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it changes the VictoriaMetrics settings of the installed release"
  return 0
fi

log "VictoriaMetrics with eight days of history"
new_namespace smoke-vm
new_namespace smoke-history
deployment smoke-history web 1
kubectl apply -f - >/dev/null <<'YAML' || fail "applying test objects"
apiVersion: apps/v1
kind: Deployment
metadata: {name: vm, namespace: smoke-vm}
spec:
  replicas: 1
  selector: {matchLabels: {app: vm}}
  template:
    metadata: {labels: {app: vm}}
    spec:
      containers:
        - name: vm
          image: quay.io/victoriametrics/victoria-metrics:v1.108.1
          args: ["-retentionPeriod=60d"]
          readinessProbe: {httpGet: {path: /health, port: 8428}, periodSeconds: 2}
---
apiVersion: v1
kind: Service
metadata: {name: vm, namespace: smoke-vm}
spec: {selector: {app: vm}, ports: [{port: 8428}]}
YAML
kubectl rollout status deploy/vm -n smoke-vm --timeout=180s >/dev/null
VM_PORT=${VM_PORT:-18428}
kubectl port-forward -n smoke-vm svc/vm "$VM_PORT:8428" >/dev/null 2>&1 &
PF_PIDS="$PF_PIDS $!"
wait_for "VictoriaMetrics reachable" curl -sf "localhost:$VM_PORT/health"

python3 - <<'PY' | curl -sf --data-binary @- "localhost:$VM_PORT/api/v1/import" || fail "importing the synthetic metrics"
import json, math, time
now = int(time.time()) // 60 * 60
start, step, ns = now - 8 * 86400, 120, "smoke-history"
def series(labels, fn):
    ts = [t for t in range(start, now + 1, step) if fn(t) is not None]
    print(json.dumps({"metric": labels, "values": [fn(t) for t in ts], "timestamps": [t * 1000 for t in ts]}))
up = lambda t: (t % 86400) / 3600 >= 14 or (t % 86400) / 3600 < 2
for pod in ["web-1", "web-2"]:
    base = {"namespace": ns, "pod": pod, "container": "web"}
    total = {"v": 0.0}
    def cpu(t, total=total):
        if not up(t): return None
        total["v"] += (0.15 + 0.1 * math.sin(2 * math.pi * t / 86400)) * step
        return round(total["v"], 3)
    series({"__name__": "container_cpu_usage_seconds_total", **base}, cpu)
    series({"__name__": "container_memory_working_set_bytes", **base}, lambda t: 256 * 2**20 if up(t) else None)
    for metric, c, m in [("kube_pod_container_resource_requests", 0.5, 512), ("kube_pod_container_resource_limits", 1, 1024)]:
        series({"__name__": metric, **base, "resource": "cpu", "unit": "core"}, lambda t, c=c: c if up(t) else None)
        series({"__name__": metric, **base, "resource": "memory", "unit": "byte"}, lambda t, m=m: m * 2**20 if up(t) else None)
    series({"__name__": "kube_pod_status_phase", "namespace": ns, "pod": pod, "phase": "Running"}, lambda t: 1 if up(t) else None)
job = {"namespace": ns, "pod": "migrate", "container": "migrate"}
series({"__name__": "kube_pod_container_resource_requests", **job, "resource": "cpu", "unit": "core"}, lambda t: 4)
series({"__name__": "kube_pod_status_phase", "namespace": ns, "pod": "migrate", "phase": "Succeeded"}, lambda t: 1)
series({"__name__": "kube_pod_status_phase", "namespace": ns, "pod": "migrate", "phase": "Running"}, lambda t: 0)
PY
curl -sf "localhost:$VM_PORT/internal/force_flush" >/dev/null
ok "imported"

log "Namespace charts cover the configured seven days"
track_call PUT /api/settings '{"integrations":{"victoriaMetrics":{"enabled":false}}}'
api PUT /api/settings '{"integrations":{"victoriaMetrics":{"enabled":true,"endpoint":"http://vm.smoke-vm.svc:8428","retentionDays":7}}}' >/dev/null \
  || fail "enabling VictoriaMetrics"
HISTORY=$(mktemp)
history_from_vm() {
  curl -sf -b "$JAR" -D "$HISTORY.h" -o "$HISTORY" "localhost:$API_PORT/api/namespaces/$1/history?range=$2" &&
    grep -qi '^x-history-source: victoriametrics' "$HISTORY.h"
}
wait_for "7 days of history from VictoriaMetrics" history_from_vm smoke-history 7d
grep -qi '^x-history-range: 7d' "$HISTORY.h" || fail "X-History-Range is not 7d"
jq -e 'length == 169' "$HISTORY" >/dev/null || fail "want 169 hourly points, got $(jq length "$HISTORY")"
jq -e '[.[].cpu.requests] | unique == ["0","1"]' "$HISTORY" >/dev/null \
  || fail "requests should be 1 core when up and 0 when down, without the finished Job: $(jq -c '[.[].cpu.requests] | unique' "$HISTORY")"
jq -e 'any(.[]; .cpu.usage == "0") and any(.[]; .cpu.usage != "0")' "$HISTORY" >/dev/null \
  || fail "scaled-down hours should read 0 and running ones more"
ok "hourly points, running and scaled-down hours, Job requests left out"
wait_for "24 hours in 10-minute steps" history_from_vm smoke-history 24h
jq -e 'length == 145' "$HISTORY" >/dev/null || fail "want 145 points for 24h, got $(jq length "$HISTORY")"
curl -sf -b "$JAR" -D "$HISTORY.h" -o "$HISTORY" "localhost:$API_PORT/api/namespaces/smoke-vm/history?range=7d" || fail "history without data"
grep -qi '^x-history-source: costdeck' "$HISTORY.h" && grep -qi '^x-history-range: 1h' "$HISTORY.h" \
  || fail "a namespace VictoriaMetrics knows nothing about should fall back to the last hour"
ok "falls back to the last hour without data"
rm -f "$HISTORY" "$HISTORY.h"
api PUT /api/settings '{"integrations":{"victoriaMetrics":{"enabled":false}}}' >/dev/null
