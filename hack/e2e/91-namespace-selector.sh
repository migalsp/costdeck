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

log "A namespace that moves to another group is left to its new owner"
MOVE_OLD_BODY='{"metadata":{"name":"smoke-move-1"},"spec":{"category":"e2e","namespaces":["smoke-move-a","smoke-move-x"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
MOVE_OLD_LEFT='{"metadata":{"name":"smoke-move-1"},"spec":{"category":"e2e","namespaces":["smoke-move-a"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
MOVE_NEW_BODY='{"metadata":{"name":"smoke-move-2"},"spec":{"category":"e2e","namespaces":["smoke-move-x"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
released_by_first() {
  local events
  events=$(kubectl get events -n "$NS" --field-selector "involvedObject.uid=$MOVE_FIRST_UID,reason=NamespaceReleased" -o jsonpath='{.items[*].message}') ||
    fail "reading the events of smoke-move-1"
  [[ "$events" == *smoke-move-x* ]]
}
for ns in smoke-move-a smoke-move-x; do
  new_namespace "$ns"
  deployment "$ns" app 1
done
kubectl rollout status deploy/app -n smoke-move-x --timeout=120s >/dev/null
track_group smoke-move-1 smoke-move-2
expect_code 201 "an older schedule for two namespaces" POST /api/scaling/groups "$MOVE_OLD_BODY" -b "$JAR"
wait_for "smoke-move-x is scaled to zero by the older schedule" replicas_are smoke-move-x app 0
MOVE_FIRST_UID=$(kubectl get scalinggroup smoke-move-1 -n "$NS" -o jsonpath='{.metadata.uid}')
expect_code 201 "a newer schedule that also lists smoke-move-x" POST /api/scaling/groups "$MOVE_NEW_BODY" -b "$JAR"
wait_for "the newer schedule reports the namespace the older one owns" group_field_is smoke-move-2 'conflictingNamespaces[0]' smoke-move-x
expect_code 200 "smoke-move-x taken out of the older schedule" PUT /api/scaling/groups/smoke-move-1 "$MOVE_OLD_LEFT" -b "$JAR"
wait_for "the newer schedule owns smoke-move-x now" group_field_is smoke-move-2 'conflictingNamespaces' ""
holds_for 10 "smoke-move-x stays down under its new schedule" replicas_are smoke-move-x app 0
released_by_first && fail "the older schedule restored smoke-move-x although the newer one owns it now"
ok "the older schedule left smoke-move-x to its new owner"

log "Deleting the older of two schedules for a namespace leaves it to the newer one"
MOVE_THIRD_BODY='{"metadata":{"name":"smoke-move-3"},"spec":{"category":"e2e","namespaces":["smoke-move-y"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
MOVE_FOURTH_BODY='{"metadata":{"name":"smoke-move-4"},"spec":{"category":"e2e","namespaces":["smoke-move-y"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
new_namespace smoke-move-y
deployment smoke-move-y app 1
kubectl rollout status deploy/app -n smoke-move-y --timeout=120s >/dev/null
track_group smoke-move-3 smoke-move-4
expect_code 201 "an older schedule for smoke-move-y" POST /api/scaling/groups "$MOVE_THIRD_BODY" -b "$JAR"
wait_for "smoke-move-y is scaled to zero" replicas_are smoke-move-y app 0
expect_code 201 "a newer schedule for the same namespace" POST /api/scaling/groups "$MOVE_FOURTH_BODY" -b "$JAR"
wait_for "the newer schedule reports the namespace the older one owns" group_field_is smoke-move-4 'conflictingNamespaces[0]' smoke-move-y
MOVE_GENERATION=$(kubectl get deploy app -n smoke-move-y -o jsonpath='{.metadata.generation}')
expect_code 204 "deleting the older schedule" DELETE /api/scaling/groups/smoke-move-3 "" -b "$JAR"
wait_for "the older schedule is gone" missing scalinggroup smoke-move-3 -n "$NS"
holds_for 6 "smoke-move-y stays down under the newer schedule" replicas_are smoke-move-y app 0
[[ "$(kubectl get deploy app -n smoke-move-y -o jsonpath='{.metadata.generation}')" == "$MOVE_GENERATION" ]] ||
  fail "deleting the older schedule scaled smoke-move-y although the newer one takes it over"
ok "the newer schedule took smoke-move-y over without it starting in between"
