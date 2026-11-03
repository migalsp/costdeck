# shellcheck shell=bash
# A schedule that pauses GitOps suspends Flux and turns off Argo CD self-heal while it keeps
# the workloads down, and resumes them afterwards. Schema-less stand-in CRDs replace the
# real tools: what matters is what CostDeck writes to their objects.

# can-i answers no for a resource type that does not exist yet, so the CRDs come first.
kubectl apply -f "$HERE/testdata/gitops-crds.yaml" >/dev/null
kubectl wait --for condition=established --timeout=60s crd/kustomizations.kustomize.toolkit.fluxcd.io crd/applications.argoproj.io >/dev/null
if [[ "$(kubectl auth can-i patch kustomizations.kustomize.toolkit.fluxcd.io --as="system:serviceaccount:$NS:$RELEASE" -A)" != yes ]]; then
  # The suite installs the chart with gitops.pause: there, a missing permission is a failure.
  [[ -n "${SKIP_INSTALL:-}" ]] || fail "installed with gitops.pause, yet the operator cannot patch Flux Kustomizations"
  echo "skip: the release was installed without gitops.pause"
  return 0
fi

GITOPS_BODY='{"metadata":{"name":"smoke-gitops"},"spec":{"category":"e2e","namespaces":["smoke-gitops"],"pauseGitOps":true,"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
field_of() { kubectl get "$1" "$2" -n smoke-gitops -o jsonpath="$3"; }
suspended_is() { [[ "$(field_of kustomization smoke-apps '{.spec.suspend}')" == "$1" ]]; }
self_heal_is() { [[ "$(field_of application smoke-shop '{.spec.syncPolicy.automated.selfHeal}')" == "$1" ]]; }
pause_mark_is() { [[ "$(field_of "$1" "$2" '{.metadata.annotations.costdeck\.io/gitops-paused}')" == "$3" ]]; }

log "A schedule that pauses GitOps suspends Flux and Argo CD self-heal before scaling down"
new_namespace smoke-gitops
kubectl apply -f - >/dev/null <<'YAML' || fail "applying the GitOps objects"
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: smoke-apps, namespace: smoke-gitops}
spec: {suspend: false}
status: {inventory: {entries: [{id: smoke-gitops_flux-app_apps_Deployment}]}}
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: {name: smoke-shop, namespace: smoke-gitops}
spec: {destination: {namespace: smoke-gitops}, syncPolicy: {automated: {selfHeal: true}}}
status: {resources: [{group: apps, kind: Deployment, namespace: smoke-gitops, name: argo-app}]}
YAML
deployment smoke-gitops flux-app 1
deployment smoke-gitops argo-app 1
kubectl label deploy flux-app -n smoke-gitops kustomize.toolkit.fluxcd.io/name=smoke-apps kustomize.toolkit.fluxcd.io/namespace=smoke-gitops >/dev/null
kubectl annotate deploy argo-app -n smoke-gitops argocd.argoproj.io/tracking-id=smoke-gitops_smoke-shop:apps/Deployment:smoke-gitops/argo-app >/dev/null
kubectl rollout status deploy/flux-app -n smoke-gitops --timeout=120s >/dev/null
track_group smoke-gitops
expect_code 201 "a closed schedule that pauses GitOps" POST /api/scaling/groups "$GITOPS_BODY" -b "$JAR"
wait_for "the Flux Kustomization is suspended" suspended_is true
wait_for "Argo CD self-heal is off" self_heal_is false
pause_mark_is kustomization smoke-apps suspend || fail "the Kustomization is not marked as paused by CostDeck"
pause_mark_is application smoke-shop self-heal || fail "the Application is not marked as paused by CostDeck"
ok "both are marked as paused by CostDeck"
wait_for "the workloads are scaled to zero" replicas_are smoke-gitops flux-app 0
wait_for "the GitOps condition says Paused" group_field_is smoke-gitops 'conditions[?(@.type=="GitOps")].reason' Paused
ok "the GitOps condition says they are paused"

log "Once everything is up again, both are resumed"
api POST /api/scaling/groups/smoke-gitops/manual '{"active":true,"until":"1h"}' >/dev/null || fail "manual override"
wait_for "the workloads come back" ready_are smoke-gitops flux-app 1
wait_for "the Kustomization is resumed" suspended_is false
wait_for "Argo CD self-heal is back on" self_heal_is true
pause_mark_is kustomization smoke-apps "" || fail "the paused mark is still on the Kustomization"
ok "the paused marks are gone"

log "Deleting a dry-run group leaves what the namespace's own schedule paused"
OWN_CONFIG_BODY='{"metadata":{"name":"smoke-gitops-own"},"spec":{"targetNamespace":"smoke-gitops-own","pauseGitOps":true,"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
OWN_DRY_BODY='{"metadata":{"name":"smoke-gitops-dry"},"spec":{"category":"e2e","namespaces":["smoke-gitops-own"],"dryRun":true,"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
own_field() { kubectl get kustomization smoke-own-apps -n smoke-gitops-own -o jsonpath="$1"; }
own_suspended_is() { [[ "$(own_field '{.spec.suspend}')" == "$1" ]]; }
new_namespace smoke-gitops-own
kubectl apply -f - >/dev/null <<'YAML' || fail "applying the Kustomization"
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata: {name: smoke-own-apps, namespace: smoke-gitops-own}
spec: {suspend: false}
status: {inventory: {entries: [{id: smoke-gitops-own_own-app_apps_Deployment}]}}
YAML
deployment smoke-gitops-own own-app 1
kubectl label deploy own-app -n smoke-gitops-own kustomize.toolkit.fluxcd.io/name=smoke-own-apps kustomize.toolkit.fluxcd.io/namespace=smoke-gitops-own >/dev/null
track_config smoke-gitops-own
expect_code 201 "a closed namespace schedule that pauses GitOps" POST /api/scaling/configs "$OWN_CONFIG_BODY" -b "$JAR"
wait_for "its Kustomization is suspended" own_suspended_is true
wait_for "its workload is scaled to zero" replicas_are smoke-gitops-own own-app 0
track_group smoke-gitops-dry
expect_code 201 "a dry-run group of the same namespace" POST /api/scaling/groups "$OWN_DRY_BODY" -b "$JAR"
wait_for "the dry run reports instead of acting" group_field_is smoke-gitops-dry phase DryRun
OWN_GENERATION=$(own_field '{.metadata.generation}')
expect_code 204 "deleting the dry run" DELETE /api/scaling/groups/smoke-gitops-dry "" -b "$JAR"
wait_for "the dry run is gone" missing scalinggroup smoke-gitops-dry -n "$NS"
own_suspended_is true || fail "deleting the dry run resumed the Kustomization its namespace schedule paused"
[[ "$(own_field '{.metadata.generation}')" == "$OWN_GENERATION" ]] ||
  fail "deleting the dry run changed the Kustomization (generation $OWN_GENERATION, now $(own_field '{.metadata.generation}'))"
[[ "$(own_field '{.metadata.annotations.costdeck\.io/gitops-paused}')" == suspend ]] || fail "the paused mark of the namespace schedule is gone"
ok "the Kustomization is still paused by the namespace schedule, untouched by the dry run"
