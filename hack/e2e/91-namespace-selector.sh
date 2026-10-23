# shellcheck shell=bash
# A schedule takes in namespaces by label as they appear, and gives back one that stops
# matching.

SEL_BODY='{"metadata":{"name":"smoke-sel"},"spec":{"category":"e2e","namespaceSelector":{"matchLabels":{"costdeck-e2e-env":"preview"}},"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
selected_has() { kubectl get scalinggroup smoke-sel -n "$NS" -o jsonpath='{.status.selectedNamespaces}' | grep -q "\"$1\""; }

log "A label selector puts the labelled namespaces on a schedule"
new_namespace smoke-sel-a
kubectl label namespace smoke-sel-a costdeck-e2e-env=preview --overwrite >/dev/null
# Two start stages, the first slow to get ready: app, then worker.
deployment smoke-sel-a app 2 15
deployment smoke-sel-a worker 1
track_config smoke-sel-a-rules
kubectl apply -f - >/dev/null <<YAML || fail "applying the workload rules"
apiVersion: finops.costdeck.io/v1
kind: ScalingConfig
metadata: {name: smoke-sel-a-rules, namespace: $NS}
spec: {targetNamespace: smoke-sel-a, sequence: [app, worker]}
YAML
kubectl rollout status deploy/app -n smoke-sel-a --timeout=120s >/dev/null
track_group smoke-sel
expect_code 201 "a closed schedule for every namespace labelled costdeck-e2e-env=preview" POST /api/scaling/groups "$SEL_BODY" -b "$JAR"
wait_for "the labelled namespace is scaled to zero" replicas_are smoke-sel-a app 0
wait_for "both of its stages" replicas_are smoke-sel-a worker 0
selected_has smoke-sel-a || fail "status.selectedNamespaces does not list smoke-sel-a"
ok "status lists the selected namespace"

log "A namespace labelled later joins at once"
new_namespace smoke-sel-b
deployment smoke-sel-b app 1
kubectl rollout status deploy/app -n smoke-sel-b --timeout=120s >/dev/null
holds_for 6 "an unlabelled namespace is left alone" replicas_are smoke-sel-b app 1
kubectl label namespace smoke-sel-b costdeck-e2e-env=preview --overwrite >/dev/null
# A settled group is requeued only every minute; the namespace event itself must trigger it.
TIMEOUT=25 wait_for "the newly labelled namespace is scaled to zero" replicas_are smoke-sel-b app 0

log "Opting out gives the namespace back"
kubectl label namespace smoke-sel-a costdeck.io/schedule=off --overwrite >/dev/null
wait_for "the opted-out namespace is restored" replicas_are smoke-sel-a app 2
# Nothing schedules it any more to start the later stage once app is ready: all at once.
TIMEOUT=10 wait_for "its later stage too, without waiting for app" replicas_are smoke-sel-a worker 1
holds_for 10 "the schedule leaves it alone" replicas_are smoke-sel-a app 2
selected_has smoke-sel-a && fail "status.selectedNamespaces still lists the opted-out namespace"
ok "status no longer lists it"

log "Selectors that would take too much, or nothing, are refused"
expect_code 400 "an empty selector, which would select every namespace" PUT /api/scaling/groups/smoke-sel \
  '{"metadata":{"name":"smoke-sel"},"spec":{"category":"e2e","namespaceSelector":{}}}' -b "$JAR"
if kubectl apply -f - >/dev/null 2>&1 <<YAML; then
apiVersion: finops.costdeck.io/v1
kind: ScalingGroup
metadata: {name: smoke-sel-empty, namespace: $NS}
spec: {category: e2e}
YAML
  kubectl delete scalinggroup smoke-sel-empty -n "$NS" --ignore-not-found >/dev/null
  fail "the CRD accepted a group with neither namespaces nor a selector"
fi
ok "the CRD refuses a group with neither namespaces nor a selector"
