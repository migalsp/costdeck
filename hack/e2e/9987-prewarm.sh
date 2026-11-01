# shellcheck shell=bash

wake_field() { kubectl get scalinggroup smoke-wake -n "$NS" -o jsonpath="{.status.$1}"; }
woken_once() { [[ -n "$(wake_field 'recentWakeUps[0].seconds')" ]]; }

log "A wake-up is timed from leaving ScaledDown until every workload is ready"
new_namespace smoke-wake
deployment smoke-wake app 1 20
kubectl rollout status deploy/app -n smoke-wake --timeout=120s >/dev/null || fail "app did not start"
track_group smoke-wake
WAKE_GROUP='{"metadata":{"name":"smoke-wake"},"spec":{"category":"e2e","namespaces":["smoke-wake"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
expect_code 201 "a closed schedule" POST /api/scaling/groups "$WAKE_GROUP" -b "$JAR"
wait_for "the namespace is scaled down" group_field_is smoke-wake phase ScaledDown
api POST /api/scaling/groups/smoke-wake/manual '{"active":true,"until":"1h"}' >/dev/null || fail "starting it by hand"
wait_for "the wake-up is recorded" woken_once
WAKE_SECONDS=$(wake_field 'recentWakeUps[0].seconds')
((WAKE_SECONDS >= 18 && WAKE_SECONDS <= 120)) || fail "the wake-up took ${WAKE_SECONDS}s, but the pod only gets ready after 20s"
[[ -z "$(wake_field wakeUpStartedAt)" ]] || fail "the wake-up is still marked as running"
ok "it took ${WAKE_SECONDS}s, the pod's 20-second readiness included"

log "Prewarm starts the namespace ahead of its schedule"
WAKE_OPENS=$(date -u -d '+5 minutes' +%H:%M 2>/dev/null || date -u -v+5M +%H:%M)
WAKE_CLOSES=$(date -u -d '+65 minutes' +%H:%M 2>/dev/null || date -u -v+65M +%H:%M)
WAKE_DAY=$(date -u -d '+5 minutes' +%w 2>/dev/null || date -u -v+5M +%w)
WAKE_SCHEDULE='[{"days":['"$WAKE_DAY"'],"startTime":"'"$WAKE_OPENS"'","endTime":"'"$WAKE_CLOSES"'","timezone":"UTC"}]'
WAKE_PREWARMED='{"metadata":{"name":"smoke-wake"},"spec":{"category":"e2e","namespaces":["smoke-wake"],"schedules":'"$WAKE_SCHEDULE"',"prewarm":{"minutes":10}}}'
expect_code 200 "a window that opens in five minutes, with ten minutes of prewarm" PUT /api/scaling/groups/smoke-wake "$WAKE_PREWARMED" -b "$JAR"
api POST /api/scaling/groups/smoke-wake/manual '{"active":null}' >/dev/null || fail "handing control back to the schedule"
wait_for "status says it is prewarming" group_field_is smoke-wake mode Prewarm
group_field_is smoke-wake prewarmMinutes 10 || fail "status.prewarmMinutes is $(wake_field prewarmMinutes), not 10"
group_field_is smoke-wake desiredState Up || fail "a prewarming namespace is not wanted up"
holds_for 10 "the workload stays up ahead of the window" replicas_are smoke-wake app 1
ok "up ten minutes ahead of a window that has not opened yet"

WAKE_PREVIEW='{"kind":"group","spec":{"namespaces":["smoke-wake"],"schedules":'"$WAKE_SCHEDULE"',"prewarm":{"minutes":10}}}'
api POST /api/scaling/preview "$WAKE_PREVIEW" | jq -e 'any(.intervals[]; .reason == "prewarm" and .active)' >/dev/null ||
  fail "the preview does not show the prewarm: $(api POST /api/scaling/preview "$WAKE_PREVIEW" | jq -c '.intervals[0:3]')"
ok "the preview shows the prewarm before the window"
