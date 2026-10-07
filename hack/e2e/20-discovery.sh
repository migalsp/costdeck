# shellcheck shell=bash
# Namespace discovery, usage collection and the FinOps API.

log "A new namespace is discovered and its usage collected"
new_namespace smoke-usage
deployment smoke-usage web 1
kubectl rollout status deploy/web -n smoke-usage --timeout=120s >/dev/null
wait_for "NamespaceFinOps created for smoke-usage" exists namespacefinops smoke-usage -n "$NS"
if kubectl get apiservice v1beta1.metrics.k8s.io -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null | grep -q True; then
  # Samples are taken every minute, and a fresh metrics-server needs a while for its first
  # reading, hence the longer wait. Requests come from the pods.
  TIMEOUT=180 wait_for "usage sampled with the pod's requests" bash -c \
    "[[ \$(kubectl get namespacefinops smoke-usage -n $NS -o jsonpath='{.status.history[-1:].cpu.requests}') == 5m ]]"
else
  echo "skip: metrics-server is not available, no usage to sample"
fi

log "The namespace shows up in the API"
api GET /api/namespaces | jq -e 'any(.[]; .spec.targetNamespace == "smoke-usage")' >/dev/null || fail "/api/namespaces lacks smoke-usage"
ok "/api/namespaces lists it"
api GET /api/finops/overview | jq -e '.currency and (.namespaces | type == "array")' >/dev/null || fail "/api/finops/overview is malformed"
wait_for "/api/finops/overview includes it" bash -c "curl -s -b '$JAR' localhost:$API_PORT/api/finops/overview | jq -e 'any(.namespaces[]; .name == \"smoke-usage\")'"
expect_code 200 "the namespace's history" GET /api/namespaces/smoke-usage/history "" -b "$JAR"
expect_code 400 "a malformed history range" GET "/api/namespaces/smoke-usage/history?range=7x" "" -b "$JAR"
expect_code 200 "recommendations" GET /api/namespaces/smoke-usage/recommendations "" -b "$JAR"

log "A deleted namespace is forgotten"
kubectl delete namespace smoke-usage --wait=false >/dev/null
wait_for "NamespaceFinOps removed with the namespace" missing namespacefinops smoke-usage -n "$NS"
