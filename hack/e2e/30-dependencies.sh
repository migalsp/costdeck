# shellcheck shell=bash
# Schedules, manual overrides and dependencies between groups. The platform's pods become
# ready only 15 s after they start and the environment's pod takes 20 s to stop, so the
# order of starting and stopping is observable instead of racing.

log "Workloads: a platform and an environment that depends on it"
new_namespace smoke-platform
new_namespace smoke-env
deployment smoke-platform db 2 15
deployment smoke-env app 1 0 20
kubectl apply -f - >/dev/null <<YAML || fail "applying test objects"
apiVersion: batch/v1
kind: CronJob
metadata: {name: report, namespace: smoke-env}
spec:
  schedule: "0 0 1 1 *"
  jobTemplate: {spec: {template: {spec: {restartPolicy: Never, containers: [{name: main, image: $PAUSE_IMAGE}]}}}}
YAML
kubectl rollout status deploy/db -n smoke-platform --timeout=120s >/dev/null
kubectl rollout status deploy/app -n smoke-env --timeout=120s >/dev/null

log "A closed schedule scales the environment down, and nothing needs the platform"
track_group smoke-env smoke-platform
kubectl apply -f - >/dev/null <<YAML || fail "applying test objects"
apiVersion: finops.costdeck.io/v1
kind: ScalingGroup
metadata: {name: smoke-platform, namespace: $NS}
spec: {category: Platform, namespaces: [smoke-platform], activation: OnDemand}
---
apiVersion: finops.costdeck.io/v1
kind: ScalingGroup
metadata: {name: smoke-env, namespace: $NS}
spec:
  category: Environments
  namespaces: [smoke-env]
  dependsOn: [smoke-platform]
  schedules: $CLOSED_SCHEDULE
YAML
wait_for "smoke-env scaled to zero" replicas_are smoke-env app 0
wait_for "its replica count recorded on the workload" bash -c \
  "[[ \$(kubectl get deploy app -n smoke-env -o jsonpath='{.metadata.annotations.costdeck\.io/original-replicas}') == 1 ]]"
wait_for "its CronJob suspended while it is down" cronjob_suspended_is smoke-env report true
wait_for "smoke-platform scaled to zero" replicas_are smoke-platform db 0
wait_for "smoke-env reports Schedule" group_field_is smoke-env mode Schedule
wait_for "smoke-env is ScaledDown" group_field_is smoke-env phase ScaledDown

log "Starting the environment starts the platform first, and waits until it is ready"
api POST /api/scaling/groups/smoke-env/manual '{"active":true,"until":"1h"}' >/dev/null || fail "manual override"
wait_for "smoke-env reports ManualUp" group_field_is smoke-env mode ManualUp
wait_for "smoke-platform started for its dependent, back at its 2 replicas" replicas_are smoke-platform db 2
wait_for "smoke-env WaitingForDependencies" group_field_is smoke-env phase WaitingForDependencies
group_field_is smoke-platform mode Dependency || fail "smoke-platform is not up in Dependency mode"
holds_for 6 "smoke-env does not start while the platform is not ready" replicas_are smoke-env app 0
wait_for "smoke-platform ScaledUp" group_field_is smoke-platform phase ScaledUp
wait_for "then smoke-env starts, back at its 1 replica" replicas_are smoke-env app 1
wait_for "smoke-env ScaledUp" group_field_is smoke-env phase ScaledUp
wait_for "its CronJob resumed" cronjob_suspended_is smoke-env report false

log "Following the schedule again stops the environment first, then the platform"
api POST /api/scaling/groups/smoke-env/manual '{"active":null}' >/dev/null || fail "clearing the override"
wait_for "smoke-env scaling down" replicas_are smoke-env app 0
holds_for 10 "the platform stays up while the environment's pod is still stopping" replicas_are smoke-platform db 2
wait_for "smoke-env ScaledDown" group_field_is smoke-env phase ScaledDown
wait_for "then smoke-platform back to zero" replicas_are smoke-platform db 0

log "An environment on its way down does not start a stopped platform"
# Up on its own first, with no dependency, so the platform stays down.
kubectl patch scalinggroup smoke-env -n "$NS" --type merge -p '{"spec":{"dependsOn":null,"active":true}}' >/dev/null
wait_for "smoke-env up on its own" group_field_is smoke-env phase ScaledUp
replicas_are smoke-platform db 0 || fail "the platform started without a dependent"
# Now it depends on the platform but goes down; its pod takes 20 s to stop.
kubectl patch scalinggroup smoke-env -n "$NS" --type merge -p '{"spec":{"dependsOn":["smoke-platform"],"active":null}}' >/dev/null
wait_for "smoke-env ScalingDown" group_field_is smoke-env phase ScalingDown
holds_for 12 "the stopped platform stays down" replicas_are smoke-platform db 0
group_field_is_not smoke-platform mode Dependency || fail "the platform was pulled up for a dependent that is going down"
wait_for "smoke-env ScaledDown" group_field_is smoke-env phase ScaledDown

log "Prometheus metrics"
wait_for "costdeck_scaling_desired_up exported for smoke-env" bash -c \
  "curl -sf localhost:$METRICS_PORT/metrics | grep -q '^costdeck_scaling_desired_up{.*smoke-env'"
