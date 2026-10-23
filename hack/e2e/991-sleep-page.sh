# shellcheck shell=bash
# A schedule with a sleep page points its namespace's HTTPRoute at Cost Deck's page while it
# is down, through the real Gateway API CRDs, and puts it back once it is up. No gateway
# runs in the test cluster: the scenario requests the page as the gateway would, with the
# header the route adds.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it installs the Gateway API CRDs and changes the dashboard address"
  return 0
fi

GATEWAY_API=${GATEWAY_API:-v1.4.0}
kubectl apply --server-side -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API/standard-install.yaml" >/dev/null ||
  fail "installing the Gateway API CRDs"
kubectl wait --for condition=established --timeout=60s crd/httproutes.gateway.networking.k8s.io crd/referencegrants.gateway.networking.k8s.io >/dev/null
if [[ "$(kubectl auth can-i patch httproutes.gateway.networking.k8s.io --as="system:serviceaccount:$NS:$RELEASE" -A)" != yes ]]; then
  # The suite installs the chart with sleepPage.enabled: there, a missing permission is a failure.
  [[ -n "${SKIP_INSTALL:-}" ]] || fail "installed with sleepPage.enabled, yet the operator cannot patch HTTPRoutes"
  echo "skip: the release was installed without sleepPage.enabled"
  return 0
fi

SLEEP_BODY='{"metadata":{"name":"smoke-sleep"},"spec":{"category":"e2e","namespaces":["smoke-sleep"],"sleepPage":true,"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
backend_of() { kubectl get httproute shop -n smoke-sleep -o jsonpath='{.spec.rules[0].backendRefs[0].name}'; }
backend_is() { [[ "$(backend_of)" == "$1" ]]; }
grant_exists() { kubectl get referencegrants.gateway.networking.k8s.io costdeck-sleep-page -n "$NS" >/dev/null 2>&1; }
no_grant() { ! grant_exists; }

log "A namespace on a schedule with a sleep page, behind an HTTPRoute"
new_namespace smoke-sleep
deployment smoke-sleep web 1
kubectl apply -f - >/dev/null <<YAML || fail "applying the Service and the HTTPRoute"
apiVersion: v1
kind: Service
metadata: {name: web, namespace: smoke-sleep}
spec: {selector: {app: web}, ports: [{port: 80, targetPort: 8080}]}
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: shop, namespace: smoke-sleep}
spec:
  parentRefs: [{name: smoke-gateway, namespace: default}]
  hostnames: [smoke-sleep.example.com]
  rules:
    - matches: [{path: {type: PathPrefix, value: /}}]
      backendRefs: [{name: web, port: 80}]
YAML
track_call PUT /api/settings '{"dashboardUrl":""}'
api PUT /api/settings '{"dashboardUrl":"https://costdeck.example.com"}' >/dev/null || fail "setting the dashboard address"
track_group smoke-sleep
expect_code 201 "a closed schedule with a sleep page" POST /api/scaling/groups "$SLEEP_BODY" -b "$JAR"
wait_for "the namespace is scaled to zero" replicas_are smoke-sleep web 0
wait_for "the route points at the sleep page" backend_is "$RELEASE-api"
grant_exists || fail "no ReferenceGrant opens the API Service to the route"
[[ "$(kubectl get httproute shop -n smoke-sleep -o jsonpath='{.spec.rules[0].backendRefs[0].namespace}')" == "$NS" ]] || fail "the route's backend is not in $NS"
ok "the route and its ReferenceGrant pass the Gateway API's own validation"

log "The page tells visitors the application is asleep and how to wake it"
page=$(curl -sS -w '\n%{http_code}' -H "X-Costdeck-Sleeping: smoke-sleep" -H "Host: smoke-sleep.example.com" -H "Accept: text/html" "localhost:$API_PORT/cart")
echo "$page" | tail -1 | grep -q 503 || fail "the sleep page should answer 503: $(echo "$page" | tail -1)"
echo "$page" | grep -q 'smoke-sleep is asleep' || fail "the page does not say the namespace is asleep"
echo "$page" | grep -q 'href="https://costdeck.example.com/wake/smoke-sleep?return=' || fail "the page has no wake link"
ok "503 with the page and a wake link"
api GET /api/wake/smoke-sleep | jq -e '.sleeping and .schedule.name == "smoke-sleep" and .hostnames == ["smoke-sleep.example.com"]' >/dev/null ||
  fail "the wake status is wrong: $(api GET /api/wake/smoke-sleep)"
ok "the dashboard's wake page knows the namespace sleeps"

log "Waking it puts the route back"
api POST /api/scaling/groups/smoke-sleep/manual '{"active":true,"until":"1h"}' >/dev/null || fail "manual override"
wait_for "the namespace is up" ready_are smoke-sleep web 1
wait_for "the route leads to the application again" backend_is web
wait_for "the ReferenceGrant is gone" no_grant
[[ -z "$(kubectl get httproute shop -n smoke-sleep -o jsonpath="{.metadata.annotations['costdeck\.io/sleeping']}")" ]] || fail "the sleeping mark stays on the route"
ok "the route is as it was"
