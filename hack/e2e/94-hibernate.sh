# shellcheck shell=bash
# Hibernate rules stop the services operators run after a namespace's workloads and start
# them before the workloads. Schema-less stand-in CRDs replace CloudNativePG and Strimzi;
# the scenario plays the operator by setting their status.

# can-i answers no for a resource type that does not exist yet, so the CRDs come first.
kubectl apply -f "$HERE/testdata/hibernate-crds.yaml" >/dev/null
kubectl wait --for condition=established --timeout=60s crd/clusters.postgresql.cnpg.io crd/kafkabridges.kafka.strimzi.io >/dev/null
if [[ "$(kubectl auth can-i patch clusters.postgresql.cnpg.io --as="system:serviceaccount:$NS:$RELEASE" -A)" != yes ]]; then
  # The suite installs the chart with hibernation.enabled: there, a missing permission is a failure.
  [[ -n "${SKIP_INSTALL:-}" ]] || fail "installed with hibernation.enabled, yet the operator cannot patch CloudNativePG clusters"
  echo "skip: the release was installed without hibernation.enabled"
  return 0
fi

HIB_RULES='[{"preset":"cnpg"},{"apiVersion":"kafka.strimzi.io/v1","kind":"KafkaBridge","replicasField":"spec.replicas","names":["smoke-*"]}]'
HIB_BODY='{"metadata":{"name":"smoke-hib"},"spec":{"category":"e2e","namespaces":["smoke-hib"],"hibernate":'"$HIB_RULES"',"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
HIB_PREVIEW='{"kind":"group","spec":{"namespaces":["smoke-hib"],"hibernate":'"$HIB_RULES"',"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
HIB_HIBERNATED='{"status":{"instances":1,"readyInstances":0,"conditions":[{"type":"cnpg.io/hibernation","status":"True","reason":"Hibernated"}]}}'
HIB_RUNNING='{"status":{"instances":1,"readyInstances":1,"conditions":[{"type":"Ready","status":"True"}]}}'
cluster_annotation() { kubectl get clusters.postgresql.cnpg.io db -n smoke-hib -o jsonpath="{.metadata.annotations.$1}"; }
hibernation_is() { [[ "$(cluster_annotation 'cnpg\.io/hibernation')" == "$1" ]]; }
marked_as() { [[ "$(cluster_annotation 'costdeck\.io/hibernated')" == "$1" ]]; }
bridge_replicas_are() { [[ "$(kubectl get kafkabridges.kafka.strimzi.io "$1" -n smoke-hib -o jsonpath='{.spec.replicas}')" == "$2" ]]; }

log "The preview lists the services a schedule would stop and leaves the operator's workloads out"
new_namespace smoke-hib
kubectl apply -f - >/dev/null <<YAML || fail "applying the services"
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata: {name: db, namespace: smoke-hib}
spec: {instances: 1}
---
apiVersion: kafka.strimzi.io/v1
kind: KafkaBridge
metadata: {name: smoke-bridge, namespace: smoke-hib}
spec: {replicas: 1}
---
apiVersion: kafka.strimzi.io/v1
kind: KafkaBridge
metadata: {name: other-bridge, namespace: smoke-hib}
spec: {replicas: 1}
YAML
kubectl patch clusters.postgresql.cnpg.io db -n smoke-hib --type merge -p "$HIB_RUNNING" >/dev/null
# app stops slowly, so the database must visibly wait for it.
deployment smoke-hib app 1 0 12
deployment smoke-hib db-1 1
uid=$(kubectl get clusters.postgresql.cnpg.io db -n smoke-hib -o jsonpath='{.metadata.uid}')
owner='{"metadata":{"ownerReferences":[{"apiVersion":"postgresql.cnpg.io/v1","kind":"Cluster","name":"db","uid":"'"$uid"'","controller":true}]}}'
kubectl patch deploy db-1 -n smoke-hib --type merge -p "$owner" >/dev/null
kubectl rollout status deploy/app -n smoke-hib --timeout=120s >/dev/null
preview=$(api POST /api/scaling/preview "$HIB_PREVIEW")
echo "$preview" | jq -e '.namespaces[0].services | map(.kind + "/" + .name) | sort == ["Cluster/db", "KafkaBridge/smoke-bridge"]' >/dev/null ||
  fail "the preview does not list the cluster and the matching bridge: $(echo "$preview" | jq -c '.namespaces[0].services')"
echo "$preview" | jq -e '.namespaces[0].workloads[] | select(.name == "db-1" and .excluded and .managedBy == "Cluster")' >/dev/null ||
  fail "the preview does not leave db-1 to its operator: $(echo "$preview" | jq -c '.namespaces[0].workloads')"
ok "the cluster and the matching bridge, and db-1 left to CloudNativePG"

log "Services stop only after the workloads are down"
track_group smoke-hib
expect_code 201 "a closed schedule with hibernate rules" POST /api/scaling/groups "$HIB_BODY" -b "$JAR"
wait_for "the workloads are scaled to zero" replicas_are smoke-hib app 0
holds_for 6 "the database waits while app is still stopping" hibernation_is ""
wait_for "the cluster is hibernated once app is gone" hibernation_is on
marked_as stopped || fail "the cluster is not marked as stopped by CostDeck"
bridge_replicas_are smoke-bridge 0 || fail "smoke-bridge was not scaled to zero"
bridge_replicas_are other-bridge 1 || fail "other-bridge was stopped although its name does not match"
replicas_are smoke-hib db-1 1 || fail "the operator's own workload was scaled by CostDeck"
ok "the cluster and smoke-bridge are stopped, other-bridge and db-1 are left alone"
holds_for 4 "the group waits for the operator to report the cluster hibernated" group_field_is_not smoke-hib phase ScaledDown
kubectl patch clusters.postgresql.cnpg.io db -n smoke-hib --type merge -p "$HIB_HIBERNATED" >/dev/null
wait_for "the group is down once it is" group_field_is smoke-hib phase ScaledDown

log "Services start before the workloads"
api POST /api/scaling/groups/smoke-hib/manual '{"active":true,"until":"1h"}' >/dev/null || fail "manual override"
wait_for "the cluster is woken" hibernation_is ""
marked_as starting || fail "the cluster is not marked as starting"
bridge_replicas_are smoke-bridge 1 || fail "smoke-bridge was not restored to one replica"
holds_for 6 "the workloads wait until the database is ready" replicas_are smoke-hib app 0
kubectl patch clusters.postgresql.cnpg.io db -n smoke-hib --type merge -p "$HIB_RUNNING" >/dev/null
wait_for "the workloads come back once it is" ready_are smoke-hib app 1
wait_for "CostDeck forgets the cluster once it runs" marked_as ""
