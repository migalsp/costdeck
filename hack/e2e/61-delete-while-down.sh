# shellcheck shell=bash

log "Deleting a group while its namespaces are down restores them"
new_namespace smoke-gone-a
new_namespace smoke-gone-b
deployment smoke-gone-a app 2
deployment smoke-gone-b app 1
kubectl apply -f - >/dev/null <<YAML || fail "applying test objects"
apiVersion: batch/v1
kind: CronJob
metadata: {name: report, namespace: smoke-gone-a}
spec:
  schedule: "0 0 1 1 *"
  jobTemplate: {spec: {template: {spec: {restartPolicy: Never, containers: [{name: main, image: $PAUSE_IMAGE}]}}}}
YAML
kubectl rollout status deploy/app -n smoke-gone-a --timeout=120s >/dev/null
kubectl rollout status deploy/app -n smoke-gone-b --timeout=120s >/dev/null
track_group smoke-gone
kubectl apply -f - >/dev/null <<YAML || fail "applying test objects"
apiVersion: finops.costdeck.io/v1
kind: ScalingGroup
metadata: {name: smoke-gone, namespace: $NS}
spec:
  category: Environments
  namespaces: [smoke-gone-a, smoke-gone-b]
  schedules: $CLOSED_SCHEDULE
YAML
wait_for "smoke-gone-a scaled to zero" replicas_are smoke-gone-a app 0
wait_for "smoke-gone-b scaled to zero" replicas_are smoke-gone-b app 0
wait_for "the CronJob suspended" cronjob_suspended_is smoke-gone-a report true

kubectl patch scalinggroup smoke-gone -n "$NS" --type merge -p '{"spec":{"namespaces":["smoke-gone-a"]}}' >/dev/null ||
  fail "dropping smoke-gone-b from the group"
wait_for "the namespace dropped from the group is back" replicas_are smoke-gone-b app 1
holds_for 6 "the namespace still in the group stays down" replicas_are smoke-gone-a app 0

expect_code 204 "deleting the group" DELETE /api/scaling/groups/smoke-gone "" -b "$JAR"
wait_for "the group is gone once it released its namespaces" missing scalinggroup smoke-gone -n "$NS"
wait_for "smoke-gone-a has its two replicas back" replicas_are smoke-gone-a app 2
wait_for "the CronJob runs again" cronjob_suspended_is smoke-gone-a report false

log "Deleting a single-namespace schedule while the namespace is down restores it"
new_namespace smoke-gone-solo
deployment smoke-gone-solo app 2
kubectl rollout status deploy/app -n smoke-gone-solo --timeout=120s >/dev/null
track_config smoke-gone-solo
expect_code 201 "schedule created through the API" POST /api/scaling/configs \
  "{\"metadata\":{\"name\":\"smoke-gone-solo\"},\"spec\":{\"targetNamespace\":\"smoke-gone-solo\",\"schedules\":$CLOSED_SCHEDULE_JSON}}" -b "$JAR"
wait_for "smoke-gone-solo scaled to zero" replicas_are smoke-gone-solo app 0
MOVED_CONFIG='{"spec":{"targetNamespace":"smoke-gone-a","schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
expect_code 422 "moving the schedule to another namespace is refused" PUT /api/scaling/configs/smoke-gone-solo "$MOVED_CONFIG" -b "$JAR"
expect_code 204 "deleting the schedule" DELETE /api/scaling/configs/smoke-gone-solo "" -b "$JAR"
wait_for "the schedule is gone once it released its namespace" missing scalingconfig smoke-gone-solo -n "$NS"
wait_for "smoke-gone-solo has its two replicas back" replicas_are smoke-gone-solo app 2
