# shellcheck shell=bash
# Idle auto-sleep against a real VictoriaMetrics fed synthetic Envoy Gateway counters and
# DCGM GPU samples: an unused namespace and one whose GPUs sit idle are scaled down inside
# their schedule's hours; one with requests, one with a busy GPU and one without any
# metrics stay up; a manual start wakes the sleeping one.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it changes the VictoriaMetrics settings of the installed release"
  return 0
fi

OPEN_SCHEDULE_JSON='[{"days":[0,1,2,3,4,5,6],"startTime":"00:00","endTime":"23:59","timezone":"UTC"}]'
idle_reason_is() { [[ "$(kubectl get scalinggroup "$1" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Idle")].reason}')" == "$2" ]]; }
up_and_scheduled() { ready_are "$1" web 1 && group_field_is "$1" mode Schedule; }

IDLE_NAMESPACES="smoke-idle-quiet smoke-idle-busy smoke-idle-dark smoke-idle-gpu smoke-idle-gpu-busy"

log "Five namespaces that sleep after a minute without use"
start_victoriametrics 7
for ns in $IDLE_NAMESPACES; do
  new_namespace "$ns"
  deployment "$ns" web 1
done
# Envoy Gateway names a route's cluster httproute/<namespace>/<route>/rule/<n>. The series
# run fifteen minutes ahead, so they stay current for the whole scenario: the quiet one
# never moves, the busy one counts five requests every fifteen seconds.
python3 - <<'PY' | curl -sf --data-binary @- "localhost:$VM_PORT/api/v1/import" || fail "importing the request counters"
import json, time
now = int(time.time()) // 15 * 15
ts = list(range(now - 600, now + 901, 15))
def requests(ns, step):
    print(json.dumps({"metric": {"__name__": "envoy_cluster_upstream_rq_total", "envoy_cluster_name": "httproute/%s/web/rule/0" % ns},
                      "values": [1000 + i * step for i in range(len(ts))], "timestamps": [t * 1000 for t in ts]}))
requests("smoke-idle-quiet", 0)
requests("smoke-idle-busy", 5)
# DCGM labels each GPU with the pod using it; no request metrics for these two.
def gpu(ns, util):
    print(json.dumps({"metric": {"__name__": "DCGM_FI_DEV_GPU_UTIL", "namespace": ns, "pod": "train-0", "gpu": "0", "modelName": "NVIDIA A10G"},
                      "values": [util] * len(ts), "timestamps": [t * 1000 for t in ts]}))
gpu("smoke-idle-gpu", 1)
gpu("smoke-idle-gpu-busy", 60)
PY
curl -sf "localhost:$VM_PORT/internal/force_flush" >/dev/null
for ns in $IDLE_NAMESPACES; do
  track_group "$ns"
  body='{"metadata":{"name":"'"$ns"'"},"spec":{"category":"e2e","namespaces":["'"$ns"'"],"idleAfter":"1m","schedules":'"$OPEN_SCHEDULE_JSON"'}}'
  expect_code 201 "$ns: an always-open schedule with idleAfter 1m" POST /api/scaling/groups "$body" -b "$JAR"
done
wait_for "the schedules are up" up_and_scheduled smoke-idle-quiet

log "The unused namespace sleeps, the others stay up"
TIMEOUT=240 wait_for "the unused namespace is scaled down" replicas_are smoke-idle-quiet web 0
group_field_is smoke-idle-quiet mode Idle || fail "mode is $(kubectl get scalinggroup smoke-idle-quiet -n "$NS" -o jsonpath='{.status.mode}')"
group_field_is_not smoke-idle-quiet idleUntil "" || fail "no status.idleUntil"
idle_reason_is smoke-idle-quiet Unused || fail "the Idle condition does not say Unused"
api GET /api/wake/smoke-idle-quiet | jq -e '.mode == "Idle" and .desiredState == "Down"' >/dev/null ||
  fail "the wake page does not know it sleeps: $(api GET /api/wake/smoke-idle-quiet)"
ok "asleep in mode Idle until $(kubectl get scalinggroup smoke-idle-quiet -n "$NS" -o jsonpath='{.status.idleUntil}')"
wait_for "the busy namespace is seen in use" idle_reason_is smoke-idle-busy InUse
wait_for "the namespace without request metrics is never treated as unused" idle_reason_is smoke-idle-dark NoTrafficData
holds_for 30 "both stay up on their schedule" sh -c "kubectl get deploy web -n smoke-idle-busy -o jsonpath='{.spec.replicas}' | grep -qx 1 &&
  kubectl get deploy web -n smoke-idle-dark -o jsonpath='{.spec.replicas}' | grep -qx 1"
up_and_scheduled smoke-idle-busy && up_and_scheduled smoke-idle-dark || fail "busy or dark left their schedule"

log "GPUs keep a namespace awake while they work, and let it sleep once they idle"
TIMEOUT=240 wait_for "the namespace whose GPU idles is scaled down" replicas_are smoke-idle-gpu web 0
group_field_is smoke-idle-gpu mode Idle || fail "smoke-idle-gpu is not asleep in mode Idle"
kubectl get scalinggroup smoke-idle-gpu -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Idle")].message}' | grep -q 'GPUs of smoke-idle-gpu stayed idle' ||
  fail "the Idle condition does not name the idle GPUs"
wait_for "the busy GPU keeps its namespace in use" idle_reason_is smoke-idle-gpu-busy InUse
up_and_scheduled smoke-idle-gpu-busy || fail "the namespace with a busy GPU left its schedule"
ok "60% GPU use counts as use, 1% does not"

log "A manual start wakes it; handed back, it sleeps again once unused"
api POST /api/scaling/groups/smoke-idle-quiet/manual '{"active":true,"until":"1h"}' >/dev/null || fail "manual override"
wait_for "the namespace is up" ready_are smoke-idle-quiet web 1
wait_for "mode is ManualUp" group_field_is smoke-idle-quiet mode ManualUp
group_field_is smoke-idle-quiet idleUntil "" || fail "status.idleUntil stays after a manual start"
idle_reason_is smoke-idle-quiet Watching || fail "the Idle condition does not say Watching"
ok "the sleep is over"
api POST /api/scaling/groups/smoke-idle-quiet/manual '{"active":null}' >/dev/null || fail "handing back to the schedule"
TIMEOUT=180 wait_for "unused again, it sleeps again" replicas_are smoke-idle-quiet web 0
group_field_is smoke-idle-quiet mode Idle || fail "not asleep in mode Idle"
ok "asleep again"
