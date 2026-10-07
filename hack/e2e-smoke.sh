#!/usr/bin/env bash
# End-to-end regression suite: installs the Helm chart into the current kube context and
# runs every scenario in hack/e2e/NN-*.sh against it. The scenarios cover the behaviour
# users depend on, so a new feature that breaks an existing one fails here:
#
#   10 dashboard and API, sign-in, API tokens, local users, RBAC
#   20 namespace discovery, usage collection and the FinOps API
#   30 schedules, manual overrides, dependencies (start and stop order), CronJobs, metrics
#   40 a stage that misses its timeout no longer holds back the next one
#   50 workload rules: start/stop order inside a namespace and exclusions
#   60 single-namespace schedules, created and deleted through the API
#   70 usage history from VictoriaMetrics            (disposable clusters only)
#   80 the MCP endpoint                               (disposable clusters only)
#
# Run it against a disposable cluster (Kind in CI, `make test-e2e` locally): it installs
# CRDs, metrics-server and changes settings. IMAGE must already be loadable by the cluster
# (kind load docker-image). SKIP_INSTALL=1 tests a release that is already installed
# instead and skips the scenarios that change cluster-wide settings, unless DISPOSABLE=1
# says that release is a test bed. E2E_ONLY=<regex>
# runs only the matching scenarios, e.g. E2E_ONLY='30|40'. Everything the scenarios
# create is named smoke-* and removed on exit.
set -euo pipefail

IMAGE=${IMAGE:-costdeck-operator:e2e}
NS=${NS:-costdeck}
RELEASE=${RELEASE:-costdeck-operator}
API_PORT=${API_PORT:-18082}
METRICS_PORT=${METRICS_PORT:-18080}
TIMEOUT=${TIMEOUT:-120}
# Settings-changing scenarios run on clusters the script installed itself, or when
# DISPOSABLE=1 says the existing release is a test bed.
DISPOSABLE=${DISPOSABLE-$([[ -z "${SKIP_INSTALL:-}" ]] && echo 1 || echo "")}
METRICS_SERVER_VERSION=v0.7.2

HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=hack/e2e/lib.sh
source "$HERE/e2e/lib.sh"

cleanup() {
  set +e
  # ${a[@]+"${a[@]}"}: an empty array is not "unbound" under set -u, even in bash 3.2.
  for call in ${CLEANUP_CALLS[@]+"${CLEANUP_CALLS[@]}"}; do
    # shellcheck disable=SC2086 # method, path and body are separate words on purpose
    api $call >/dev/null 2>&1
  done
  ((${#CLEANUP_GROUPS[@]})) && kubectl delete scalinggroup "${CLEANUP_GROUPS[@]}" -n "$NS" --ignore-not-found --wait=false >/dev/null 2>&1
  ((${#CLEANUP_CONFIGS[@]})) && kubectl delete scalingconfig "${CLEANUP_CONFIGS[@]}" -n "$NS" --ignore-not-found --wait=false >/dev/null 2>&1
  ((${#CLEANUP_NAMESPACES[@]})) && kubectl delete namespace "${CLEANUP_NAMESPACES[@]}" --ignore-not-found --wait=false >/dev/null 2>&1
  if [[ -n "${PF_PIDS:-}" ]]; then
    kill $PF_PIDS 2>/dev/null
    wait $PF_PIDS 2>/dev/null
  fi
  [[ -n "${JAR:-}" ]] && rm -f "$JAR"
}
trap cleanup EXIT

echo "kube context: $(kubectl config current-context)"
if [[ -z "${SKIP_INSTALL:-}" ]]; then
  log "Installing chart with $IMAGE"
  helm upgrade --install "$RELEASE" deploy/helm/costdeck-operator -n "$NS" --create-namespace \
    --set image.repository="${IMAGE%:*}" --set image.tag="${IMAGE##*:}" --set image.pullPolicy=Never \
    --wait --timeout 3m
  if ! kubectl get apiservice v1beta1.metrics.k8s.io >/dev/null 2>&1; then
    log "Installing metrics-server $METRICS_SERVER_VERSION"
    kubectl apply -f "https://github.com/kubernetes-sigs/metrics-server/releases/download/$METRICS_SERVER_VERSION/components.yaml" >/dev/null
    # Kind's kubelets serve self-signed certificates.
    kubectl patch deploy metrics-server -n kube-system --type json \
      -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]' >/dev/null
    kubectl rollout status deploy/metrics-server -n kube-system --timeout=180s
  fi
fi
kubectl rollout status "deploy/$RELEASE" -n "$NS" --timeout=120s

log "Signing in to the API"
kubectl port-forward -n "$NS" "svc/$RELEASE-api" "$API_PORT:8082" >/dev/null 2>&1 &
PF_PIDS="$!"
kubectl port-forward -n "$NS" "svc/$RELEASE-metrics" "$METRICS_PORT:8080" >/dev/null 2>&1 &
PF_PIDS="$PF_PIDS $!"
wait_for "API reachable" curl -sf "localhost:$API_PORT/api/auth/config"
ADMIN_USER=$(kubectl get secret "$RELEASE-admin-credentials" -n "$NS" -o jsonpath='{.data.username}' | base64 -d)
ADMIN_PASSWORD=$(kubectl get secret "$RELEASE-admin-credentials" -n "$NS" -o jsonpath='{.data.password}' | base64 -d)
JAR=$(mktemp)
curl -sf -c "$JAR" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASSWORD\"}" "localhost:$API_PORT/api/login" >/dev/null \
  || fail "login with the generated admin password"
ok "signed in as $ADMIN_USER"

for scenario in "$HERE"/e2e/[0-9]*.sh; do
  name=$(basename "$scenario" .sh)
  [[ -z "${E2E_ONLY:-}" || "$name" =~ $E2E_ONLY ]] || continue
  printf '\n\033[1;34m### %s\033[0m\n' "$name"
  # shellcheck source=/dev/null
  source "$scenario"
done

log "All scenarios passed"
