# shellcheck shell=bash
# Argo Rollouts with the real controller: a canary Rollout and a Rollout that takes its
# pods from a Deployment (workloadRef). A schedule scales both to zero and back, and leaves
# the referenced Deployment at zero, so no copy of the Rollout's pods ever starts.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it installs Argo Rollouts"
  return 0
fi

ARGO_ROLLOUTS=${ARGO_ROLLOUTS:-v1.10.0}
rollout_field() { kubectl get rollout "$1" -n smoke-rollouts -o jsonpath="{$2}"; }
rollout_ready() { [[ "$(rollout_field "$1" .status.readyReplicas)" == "$2" ]]; }
rollout_replicas() { [[ "$(rollout_field "$1" .spec.replicas)" == "$2" ]]; }
pods_of() { kubectl get pods -n smoke-rollouts -l "app=$1" --field-selector=status.phase!=Succeeded,status.phase!=Failed -o name | wc -l | tr -d ' '; }
no_pods() { [[ "$(pods_of "$1")" == 0 ]]; }
# The Rollout's own pods carry the Deployment's labels, so the Deployment is judged by its
# own replicas: spec, the annotation and what its ReplicaSets run.
legacy_untouched() {
  [[ "$(kubectl get deploy legacy -n smoke-rollouts -o jsonpath='{.spec.replicas}')" == 0 ]] &&
    [[ -z "$(kubectl get deploy legacy -n smoke-rollouts -o jsonpath="{.metadata.annotations['costdeck\.io/original-replicas']}")" ]] &&
    [[ "$(kubectl get deploy legacy -n smoke-rollouts -o jsonpath='{.status.replicas}')" =~ ^0?$ ]]
}

log "Argo Rollouts runs a canary Rollout and one that takes its pods from a Deployment"
kubectl create namespace argo-rollouts --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl apply --server-side -n argo-rollouts -f "https://github.com/argoproj/argo-rollouts/releases/download/$ARGO_ROLLOUTS/install.yaml" >/dev/null ||
  fail "installing Argo Rollouts"
kubectl wait --for condition=established --timeout=60s crd/rollouts.argoproj.io >/dev/null
kubectl rollout status -n argo-rollouts deploy/argo-rollouts --timeout=180s >/dev/null || fail "Argo Rollouts did not start"
[[ "$(kubectl auth can-i patch rollouts.argoproj.io --as="system:serviceaccount:$NS:$RELEASE" -A)" == yes ]] ||
  fail "the operator cannot patch Rollouts"
new_namespace smoke-rollouts
kubectl apply -f - >/dev/null <<YAML || fail "applying the Rollouts"
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata: {name: web, namespace: smoke-rollouts}
spec:
  replicas: 2
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers: [{name: main, image: $PAUSE_IMAGE, resources: {requests: {cpu: 5m, memory: 8Mi}}}]
  strategy:
    canary:
      steps: [{setWeight: 50}, {pause: {duration: 1}}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: legacy, namespace: smoke-rollouts}
spec:
  replicas: 0
  selector: {matchLabels: {app: legacy}}
  template:
    metadata: {labels: {app: legacy}}
    spec:
      containers: [{name: main, image: $PAUSE_IMAGE, resources: {requests: {cpu: 5m, memory: 8Mi}}}]
---
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata: {name: ref, namespace: smoke-rollouts}
spec:
  replicas: 1
  selector: {matchLabels: {app: legacy}}
  workloadRef: {apiVersion: apps/v1, kind: Deployment, name: legacy}
  strategy:
    canary:
      steps: [{setWeight: 100}]
YAML
wait_for "the canary Rollout is ready" rollout_ready web 2
wait_for "the workloadRef Rollout is ready" rollout_ready ref 1

log "Previews and the namespace page show the Rollouts, not the referenced Deployment"
ROLLOUT_PREVIEW='{"kind":"group","spec":{"namespaces":["smoke-rollouts"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
preview=$(api POST /api/scaling/preview "$ROLLOUT_PREVIEW")
echo "$preview" | jq -e '[.namespaces[] | select(.name == "smoke-rollouts") | .workloads[] | "\(.kind)/\(.name)/\(.replicas)"] | sort == ["Rollout/ref/1","Rollout/web/2"]' >/dev/null ||
  fail "the preview lists the wrong workloads: $(echo "$preview" | jq -c '.namespaces[].workloads')"
api GET /api/namespaces/smoke-rollouts/workloads | jq -e '[.[] | .kind] | unique == ["Rollout"]' >/dev/null ||
  fail "the namespace page lists: $(api GET /api/namespaces/smoke-rollouts/workloads)"
ok "two Rollouts, no Deployment"

log "A closed schedule scales both Rollouts to zero"
ROLLOUT_GROUP='{"metadata":{"name":"smoke-rollouts"},"spec":{"category":"e2e","namespaces":["smoke-rollouts"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
track_group smoke-rollouts
expect_code 201 "a closed schedule" POST /api/scaling/groups "$ROLLOUT_GROUP" -b "$JAR"
wait_for "the canary Rollout is at zero" rollout_replicas web 0
wait_for "the workloadRef Rollout is at zero" rollout_replicas ref 0
wait_for "no pod of either is left" sh -c "kubectl get pods -n smoke-rollouts --field-selector=status.phase!=Succeeded,status.phase!=Failed -o name | grep -q . && exit 1 || exit 0"
[[ "$(rollout_field web ".metadata.annotations['costdeck\.io/original-replicas']")" == 2 ]] || fail "web does not record its two replicas"
wait_for "the group is ScaledDown" group_field_is smoke-rollouts phase ScaledDown
legacy_untouched || fail "the referenced Deployment was changed"
ok "the referenced Deployment was left alone"

log "Waking it brings both back, and still no copy of the Rollout's pods"
api POST /api/scaling/groups/smoke-rollouts/manual '{"active":true,"until":"1h"}' >/dev/null || fail "manual override"
wait_for "the canary Rollout has its two replicas" rollout_ready web 2
wait_for "the workloadRef Rollout has its replica" rollout_ready ref 1
wait_for "the group is ScaledUp" group_field_is smoke-rollouts phase ScaledUp
holds_for 15 "the referenced Deployment stays at zero" legacy_untouched
[[ -z "$(rollout_field web ".metadata.annotations['costdeck\.io/original-replicas']")" ]] || fail "the annotation stays on web"
