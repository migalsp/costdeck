# shellcheck shell=bash
# After a scale-down, the realized-savings check says whether the nodes the workloads ran
# on went away and names what keeps them. The test cluster's nodes never go, so the
# scenario plants blockers next to the workloads and expects them named.

REAL_BODY='{"metadata":{"name":"smoke-real"},"spec":{"category":"e2e","namespaces":["smoke-real"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
realized() { api GET /api/scaling/groups/smoke-real/realized; }
realized_state_is() { [[ "$(realized | jq -r .state)" == "$1" ]]; }

log "The check records the nodes a scale-down should free"
new_namespace smoke-real
new_namespace smoke-real-blockers
deployment smoke-real app 1
kubectl rollout status deploy/app -n smoke-real --timeout=120s >/dev/null
node=$(kubectl get pods -n smoke-real -l app=app -o jsonpath='{.items[0].spec.nodeName}')
# Next to it: a bare pod with a disk-backed emptyDir that Cluster Autoscaler may not evict,
# and a database whose PodDisruptionBudget allows no disruption.
kubectl apply -f - >/dev/null <<YAML || fail "applying the blockers"
apiVersion: v1
kind: Pod
metadata:
  name: debug
  namespace: smoke-real-blockers
  annotations: {cluster-autoscaler.kubernetes.io/safe-to-evict: "false"}
spec:
  nodeName: $node
  containers: [{name: main, image: $PAUSE_IMAGE, resources: {requests: {cpu: 5m, memory: 8Mi}}}]
  volumes: [{name: scratch, emptyDir: {}}]
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: {name: db, namespace: smoke-real-blockers}
spec: {minAvailable: 1, selector: {matchLabels: {app: db}}}
YAML
deployment smoke-real-blockers db 1
kubectl rollout status deploy/db -n smoke-real-blockers --timeout=120s >/dev/null
db_node=$(kubectl get pods -n smoke-real-blockers -l app=db -o jsonpath='{.items[0].spec.nodeName}')

track_group smoke-real
expect_code 201 "a closed schedule" POST /api/scaling/groups "$REAL_BODY" -b "$JAR"
wait_for "the workloads are scaled to zero" replicas_are smoke-real app 0
wait_for "the check is running" realized_state_is settling
check=$(realized)
echo "$check" | jq -e --arg n "$node" '.nodes | map(.name) | index($n) != null' >/dev/null ||
  fail "the check does not record $node, where app ran: $check"
echo "$check" | jq -e '.gone == 0 and .realizedHourly == 0 and .settleMinutes == 15' >/dev/null ||
  fail "a node that is still there counts as saved: $(echo "$check" | jq -c '{gone, realizedHourly, settleMinutes}')"
ok "it records $node, not yet freed"
blockers=$(echo "$check" | jq -c --arg n "$node" '[.nodes[] | select(.name == $n) | .blockers[] | select(.pod != null and (.pod | startswith("smoke-real-blockers/"))) | .pod + " " + .reason] | sort')
# On a cluster of several nodes the database may run elsewhere.
if [[ "$db_node" == "$node" ]]; then
  echo "$blockers" | jq -e 'any(test("smoke-real-blockers/db-.* pdb"))' >/dev/null || fail "the PodDisruptionBudget is not named: $blockers"
fi
for b in "smoke-real-blockers/debug local-storage" "smoke-real-blockers/debug no-controller" "smoke-real-blockers/debug safe-to-evict"; do
  echo "$blockers" | jq -e --arg b "$b" 'index($b) != null' >/dev/null || fail "the check does not name $b: $blockers"
done
ok "it names what keeps $node: $blockers"

log "Starting the schedule again ends the check"
api POST /api/scaling/groups/smoke-real/manual '{"active":true,"until":"1h"}' >/dev/null || fail "manual override"
wait_for "there is nothing to check once it is up" realized_state_is none
