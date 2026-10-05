#!/usr/bin/env bash
# End-to-end smoke test: installs the Helm chart into the current kube context and checks
# the behaviour users depend on.
#
#   - a schedule that is closed scales a namespace to zero
#   - a manual override through the API brings it back, and "follow schedule" ends it
#   - an on-demand group starts only while a dependent group needs it
#   - the operator cannot read Secrets outside its own namespace
#   - Prometheus metrics are exported
#
# Run it against a disposable cluster (Kind in CI): it installs CRDs and creates
# namespaces. IMAGE must already be loadable by the cluster (kind load docker-image).
# SKIP_INSTALL=1 tests a release that is already installed instead; the smoke-*
# namespaces and groups are removed afterwards either way.
set -euo pipefail

IMAGE=${IMAGE:-costdeck-operator:e2e}
NS=${NS:-costdeck}
RELEASE=${RELEASE:-costdeck-operator}
API_PORT=${API_PORT:-18082}
METRICS_PORT=${METRICS_PORT:-18080}
TIMEOUT=${TIMEOUT:-120}

log() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
fail() { printf '\033[31mFAIL: %s\033[0m\n' "$*" >&2; dump; exit 1; }

dump() {
  echo "--- scaling groups"; kubectl get scalinggroups -n "$NS" -o wide || true
  echo "--- operator logs"; kubectl logs -n "$NS" "deploy/$RELEASE" --tail=80 || true
}

# wait_for <description> <command...>: retries the command until it succeeds.
wait_for() {
  local what=$1; shift
  local deadline=$((SECONDS + TIMEOUT))
  until "$@" >/dev/null 2>&1; do
    ((SECONDS < deadline)) || fail "timed out waiting for: $what"
    sleep 3
  done
  echo "ok: $what"
}

replicas_are() { [[ "$(kubectl get deploy "$2" -n "$1" -o jsonpath='{.spec.replicas}')" == "$3" ]]; }
group_field_is() { [[ "$(kubectl get scalinggroup "$1" -n "$NS" -o jsonpath="{.status.$2}")" == "$3" ]]; }

cleanup() {
  if [[ -n "${PF_PIDS:-}" ]]; then
    kill $PF_PIDS 2>/dev/null || true
    wait $PF_PIDS 2>/dev/null || true
  fi
  kubectl delete scalinggroup smoke-env smoke-platform -n "$NS" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete namespace smoke-env smoke-platform --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

if [[ -z "${SKIP_INSTALL:-}" ]]; then
  log "Installing chart with $IMAGE"
  helm upgrade --install "$RELEASE" deploy/helm/costdeck-operator -n "$NS" --create-namespace \
    --set image.repository="${IMAGE%:*}" --set image.tag="${IMAGE##*:}" --set image.pullPolicy=Never \
    --wait --timeout 3m
fi
kubectl rollout status "deploy/$RELEASE" -n "$NS" --timeout=120s

log "Creating workloads"
for ns in smoke-platform smoke-env; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
done
kubectl apply -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata: {name: db, namespace: smoke-platform}
spec:
  replicas: 2
  selector: {matchLabels: {app: db}}
  template:
    metadata: {labels: {app: db}}
    spec: {containers: [{name: pause, image: registry.k8s.io/pause:3.10, resources: {requests: {cpu: 5m, memory: 8Mi}}}]}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: app, namespace: smoke-env}
spec:
  replicas: 1
  selector: {matchLabels: {app: app}}
  template:
    metadata: {labels: {app: app}}
    spec: {containers: [{name: pause, image: registry.k8s.io/pause:3.10, resources: {requests: {cpu: 5m, memory: 8Mi}}}]}
EOF
kubectl rollout status deploy/db -n smoke-platform --timeout=120s
kubectl rollout status deploy/app -n smoke-env --timeout=120s

log "A closed schedule scales the environment down, and nothing needs the platform"
kubectl apply -f - <<EOF
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
  # A one-minute window a year away from "now" for practical purposes: never open in CI.
  schedules: [{startDay: 0, startTime: "03:00", endDay: 0, endTime: "03:01", timezone: "Pacific/Kiritimati"}]
EOF
wait_for "smoke-env scaled to zero" replicas_are smoke-env app 0
wait_for "smoke-platform scaled to zero" replicas_are smoke-platform db 0
wait_for "smoke-env reports Schedule/Down" group_field_is smoke-env mode Schedule

log "Signing in to the API"
kubectl port-forward -n "$NS" "svc/$RELEASE-api" "$API_PORT:8082" >/dev/null 2>&1 &
PF_PIDS="$!"
kubectl port-forward -n "$NS" "svc/$RELEASE-metrics" "$METRICS_PORT:8080" >/dev/null 2>&1 &
PF_PIDS="$PF_PIDS $!"
wait_for "API reachable" curl -sf "localhost:$API_PORT/api/auth/config"
ADMIN_USER=$(kubectl get secret "$RELEASE-admin-credentials" -n "$NS" -o jsonpath='{.data.username}' | base64 -d)
PASSWORD=$(kubectl get secret "$RELEASE-admin-credentials" -n "$NS" -o jsonpath='{.data.password}' | base64 -d)
JAR=$(mktemp)
curl -sf -c "$JAR" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$PASSWORD\"}" "localhost:$API_PORT/api/login" >/dev/null \
  || fail "login with the generated admin password"
code=$(curl -s -o /dev/null -w '%{http_code}' "localhost:$API_PORT/api/scaling/groups")
[[ "$code" == 401 ]] || fail "unauthenticated API call returned $code, want 401"
echo "ok: API requires authentication"

log "Starting the environment manually starts the platform first"
curl -sf -b "$JAR" -H 'Content-Type: application/json' -d '{"active":true,"until":"1h"}' \
  "localhost:$API_PORT/api/scaling/groups/smoke-env/manual" >/dev/null || fail "manual override"
wait_for "smoke-env reports ManualUp" group_field_is smoke-env mode ManualUp
wait_for "smoke-platform kept up for its dependent" group_field_is smoke-platform mode Dependency
wait_for "platform restored to its 2 replicas" replicas_are smoke-platform db 2
wait_for "environment restored to 1 replica" replicas_are smoke-env app 1

log "Following the schedule again scales both down"
curl -sf -b "$JAR" -H 'Content-Type: application/json' -d '{"active":null}' \
  "localhost:$API_PORT/api/scaling/groups/smoke-env/manual" >/dev/null || fail "clearing the override"
wait_for "smoke-env back to zero" replicas_are smoke-env app 0
wait_for "smoke-platform back to zero" replicas_are smoke-platform db 0

log "RBAC: Secrets only in the operator namespace"
SA="system:serviceaccount:$NS:$RELEASE"
[[ "$(kubectl auth can-i list secrets --as="$SA" -A)" == no ]] || fail "operator can list Secrets cluster-wide"
[[ "$(kubectl auth can-i get secrets --as="$SA" -n "$NS")" == yes ]] || fail "operator cannot read its own Secrets"
echo "ok: Secret access is namespaced"

log "Prometheus metrics"
wait_for "costdeck_scaling_desired_up exported" bash -c "curl -sf localhost:$METRICS_PORT/metrics | grep -q '^costdeck_scaling_desired_up{.*smoke-env'"

log "Smoke test passed"
