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
wait_for "the route points at the sleep page" backend_is "$RELEASE-sleep"
grant_exists || fail "no ReferenceGrant opens the sleep page Service to the route"
[[ "$(kubectl get referencegrants.gateway.networking.k8s.io costdeck-sleep-page -n "$NS" -o jsonpath='{.spec.to[*].name}')" == "$RELEASE-sleep" ]] ||
  fail "the ReferenceGrant opens more than the sleep page Service: $(kubectl get referencegrants.gateway.networking.k8s.io costdeck-sleep-page -n "$NS" -o jsonpath='{.spec.to}')"
[[ "$(kubectl get httproute shop -n smoke-sleep -o jsonpath='{.spec.rules[0].backendRefs[0].namespace}')" == "$NS" ]] || fail "the route's backend is not in $NS"
ok "the route and its ReferenceGrant pass the Gateway API's own validation"

log "The page tells visitors the application is asleep and how to wake it"
SLEEP_PORT=${SLEEP_PORT:-18083}
kubectl port-forward -n "$NS" "svc/$RELEASE-sleep" "$SLEEP_PORT:80" >/dev/null 2>&1 &
PF_PIDS="$PF_PIDS $!"
wait_for "the sleep page Service answers" curl -s -o /dev/null "localhost:$SLEEP_PORT/"
page=$(curl -sS -w '\n%{http_code}' -H "X-Costdeck-Sleeping: smoke-sleep" -H "Host: smoke-sleep.example.com" -H "Accept: text/html" "localhost:$SLEEP_PORT/cart")
echo "$page" | tail -1 | grep -q 503 || fail "the sleep page should answer 503: $(echo "$page" | tail -1)"
echo "$page" | grep -q 'smoke-sleep is asleep' || fail "the page does not say the namespace is asleep"
echo "$page" | grep -q 'href="https://costdeck.example.com/wake/smoke-sleep?return=' || fail "the page has no wake link"
ok "503 with the page and a wake link"
for path in /api/settings /api/version /mcp /; do
  through=$(curl -sS -o /dev/null -w '%{http_code}' -H "X-Costdeck-Sleeping: smoke-sleep" -H "Accept: application/json" "localhost:$SLEEP_PORT$path")
  [[ "$through" == 503 ]] || fail "$path through the sleep page Service answered $through, not the sleep page"
done
ok "a route to the sleep page Service reaches nothing but the page"
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

log "A namespace with only a route that leaves a sleeping group gets its route back"
SLEEP_PAIR_BODY='{"metadata":{"name":"smoke-sleep-pair"},"spec":{"category":"e2e","namespaces":["smoke-sleep-mate","smoke-sleep-routes"],"sleepPage":true,"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
SLEEP_PAIR_LEFT='{"metadata":{"name":"smoke-sleep-pair"},"spec":{"category":"e2e","namespaces":["smoke-sleep-mate"],"sleepPage":true,"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
route_backend_in() { kubectl get httproute web -n "$1" -o jsonpath='{.spec.rules[0].backendRefs[0].name}'; }
route_backend_is() { [[ "$(route_backend_in "$1")" == "$2" ]]; }
for ns in smoke-sleep-mate smoke-sleep-routes; do
  new_namespace "$ns"
  kubectl apply -f - >/dev/null <<YAML || fail "applying the HTTPRoute in $ns"
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: web, namespace: $ns}
spec:
  parentRefs: [{name: smoke-gateway, namespace: default}]
  hostnames: [$ns.example.com]
  rules:
    - backendRefs: [{name: web, port: 80}]
YAML
done
track_group smoke-sleep-pair
expect_code 201 "a closed schedule with a sleep page for two namespaces without workloads" POST /api/scaling/groups "$SLEEP_PAIR_BODY" -b "$JAR"
wait_for "the route of smoke-sleep-routes points at the sleep page" route_backend_is smoke-sleep-routes "$RELEASE-sleep"
wait_for "the route of smoke-sleep-mate points at the sleep page" route_backend_is smoke-sleep-mate "$RELEASE-sleep"
expect_code 200 "smoke-sleep-routes taken out of the group while it sleeps" PUT /api/scaling/groups/smoke-sleep-pair "$SLEEP_PAIR_LEFT" -b "$JAR"
wait_for "the route of the namespace that left leads to its application again" route_backend_is smoke-sleep-routes web
holds_for 6 "the namespace still in the group keeps sleeping" route_backend_is smoke-sleep-mate "$RELEASE-sleep"

log "A namespace handed to a schedule without a sleep page gets its route back"
SLEEP_HEIR_BODY='{"metadata":{"name":"smoke-sleep-heir"},"spec":{"category":"e2e","namespaces":["smoke-sleep-mate"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
SLEEP_PAIR_SWAPPED='{"metadata":{"name":"smoke-sleep-pair"},"spec":{"category":"e2e","namespaces":["smoke-sleep-routes"],"sleepPage":true,"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
track_group smoke-sleep-heir
expect_code 201 "a newer schedule without a sleep page that also lists smoke-sleep-mate" POST /api/scaling/groups "$SLEEP_HEIR_BODY" -b "$JAR"
wait_for "it reports the namespace the older schedule owns" group_field_is smoke-sleep-heir 'conflictingNamespaces[0]' smoke-sleep-mate
expect_code 200 "smoke-sleep-mate handed over by the older schedule" PUT /api/scaling/groups/smoke-sleep-pair "$SLEEP_PAIR_SWAPPED" -b "$JAR"
wait_for "the handed-over route leads to its application again" route_backend_is smoke-sleep-mate web
wait_for "the namespace that joined the older schedule sleeps" route_backend_is smoke-sleep-routes "$RELEASE-sleep"

log "A namespace handed to a schedule that is up and has its own sleep page wakes at once"
SLEEP_ALWAYS_OPEN='[{"days":[0,1,2,3,4,5,6],"startTime":"00:00","endTime":"23:59","timezone":"UTC"}]'
SLEEP_PAIR_GROWN='{"metadata":{"name":"smoke-sleep-pair"},"spec":{"category":"e2e","namespaces":["smoke-sleep-routes","smoke-sleep-woken"],"sleepPage":true,"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
SLEEP_AWAKE_BODY='{"metadata":{"name":"smoke-sleep-awake"},"spec":{"category":"e2e","namespaces":["smoke-sleep-woken"],"sleepPage":true,"schedules":'"$SLEEP_ALWAYS_OPEN"'}}'
new_namespace smoke-sleep-woken
kubectl apply -f - >/dev/null <<YAML || fail "applying the HTTPRoute in smoke-sleep-woken"
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: web, namespace: smoke-sleep-woken}
spec:
  parentRefs: [{name: smoke-gateway, namespace: default}]
  hostnames: [smoke-sleep-woken.example.com]
  rules:
    - backendRefs: [{name: web, port: 80}]
YAML
expect_code 200 "smoke-sleep-woken added to the older schedule while it sleeps" PUT /api/scaling/groups/smoke-sleep-pair "$SLEEP_PAIR_GROWN" -b "$JAR"
wait_for "the route of smoke-sleep-woken points at the sleep page" route_backend_is smoke-sleep-woken "$RELEASE-sleep"
track_group smoke-sleep-awake
expect_code 201 "a newer schedule that is up, with a sleep page, that also lists smoke-sleep-woken" POST /api/scaling/groups "$SLEEP_AWAKE_BODY" -b "$JAR"
wait_for "it reports the namespace the older schedule owns" group_field_is smoke-sleep-awake 'conflictingNamespaces[0]' smoke-sleep-woken
wait_for "it is up" group_field_is smoke-sleep-awake phase ScaledUp
expect_code 200 "smoke-sleep-woken handed over by the older schedule" PUT /api/scaling/groups/smoke-sleep-pair "$SLEEP_PAIR_SWAPPED" -b "$JAR"
TIMEOUT=15 wait_for "the new owner wakes the route without waiting for its next periodic check" route_backend_is smoke-sleep-woken web
