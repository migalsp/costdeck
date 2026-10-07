# shellcheck shell=bash
# Workload rules of a namespace in a group: stages of workloads start in order and stop in
# reverse, the most specific pattern wins, and excluded workloads are never touched.

log "An operator in stage 2 after '*' stops before what it manages, and starts after it"
new_namespace smoke-rules
deployment smoke-rules operator 1 0 20   # takes 20 s to stop
deployment smoke-rules app 1 10          # ready 10 s after it starts
deployment smoke-rules keep 1
kubectl rollout status deploy/app -n smoke-rules --timeout=120s >/dev/null
# The rules are created the way the dashboard does it, before the group exists.
track_config smoke-rules-rules
expect_code 201 "workload rules created through the API" POST /api/scaling/configs \
  '{"metadata":{"name":"smoke-rules-rules"},"spec":{"targetNamespace":"smoke-rules","sequence":["*","operator"],"exclusions":["keep"]}}' -b "$JAR"
track_group smoke-rules
kubectl apply -f - >/dev/null <<YAML || fail "applying test objects"
apiVersion: finops.costdeck.io/v1
kind: ScalingGroup
metadata: {name: smoke-rules, namespace: $NS}
spec: {category: Smoke, namespaces: [smoke-rules], schedules: $CLOSED_SCHEDULE}
YAML
wait_for "the operator stops first" replicas_are smoke-rules operator 0
holds_for 10 "the rest waits while the operator's pod is still stopping" replicas_are smoke-rules app 1
wait_for "then the rest stops" replicas_are smoke-rules app 0
wait_for "the group ScaledDown" group_field_is smoke-rules phase ScaledDown
replicas_are smoke-rules keep 1 || fail "an excluded workload was scaled"
ok "the excluded workload kept its replica"

api POST /api/scaling/groups/smoke-rules/manual '{"active":true,"until":"1h"}' >/dev/null || fail "manual override"
wait_for "'*' starts first" replicas_are smoke-rules app 1
holds_for 6 "the operator waits until the rest is ready" replicas_are smoke-rules operator 0
wait_for "then the operator starts" replicas_are smoke-rules operator 1
