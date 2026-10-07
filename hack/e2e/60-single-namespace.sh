# shellcheck shell=bash
# A schedule for a single namespace (ScalingConfig), managed through the API.

log "A single-namespace schedule scales its namespace and can be deleted"
new_namespace smoke-solo
deployment smoke-solo app 1
kubectl rollout status deploy/app -n smoke-solo --timeout=120s >/dev/null
track_config smoke-solo
expect_code 201 "schedule created through the API" POST /api/scaling/configs \
  "{\"metadata\":{\"name\":\"smoke-solo\"},\"spec\":{\"targetNamespace\":\"smoke-solo\",\"schedules\":$CLOSED_SCHEDULE_JSON}}" -b "$JAR"
wait_for "smoke-solo scaled to zero" replicas_are smoke-solo app 0
wait_for "the schedule ScaledDown" bash -c "[[ \$(kubectl get scalingconfig smoke-solo -n $NS -o jsonpath='{.status.phase}') == ScaledDown ]]"
api POST /api/scaling/configs/smoke-solo/manual '{"active":true,"until":"1h"}' >/dev/null || fail "manual override"
wait_for "Start now brings it back" replicas_are smoke-solo app 1
expect_code 204 "deleting the schedule" DELETE /api/scaling/configs/smoke-solo "" -b "$JAR"
wait_for "the schedule is gone" missing scalingconfig smoke-solo -n "$NS"
holds_for 6 "the namespace keeps its current size" replicas_are smoke-solo app 1
