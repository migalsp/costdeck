# shellcheck shell=bash
# Several clusters: the installation reads itself through its own Service as another
# cluster, with a real viewer API token, next to a cluster that cannot be reached.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it changes the clusters setting and creates an API token"
  return 0
fi

clusters() { api GET /api/clusters; }
row() { clusters | jq -c --arg n "$1" '.clusters[] | select(.name == $n)'; }
mirrored() { [[ "$(row mirror | jq -r '.summary.nodes // empty')" == "$(row "$LOCAL_NAME" | jq -r '.summary.nodes')" ]]; }

log "A viewer token reads this installation's summary"
HUB_TOKEN=$(api POST /api/tokens '{"name":"smoke-hub","role":"viewer","expiresInDays":1}' | jq -r .token)
track_call DELETE /api/tokens/smoke-hub
[[ "$HUB_TOKEN" == cdk_* ]] || fail "no API token"
summary=$(curl -sf -H "Authorization: Bearer $HUB_TOKEN" "localhost:$API_PORT/api/finops/summary") || fail "the summary refused a viewer token"
echo "$summary" | jq -e '.nodes > 0 and .monthlyCost > 0 and (.schedules | type) == "number"' >/dev/null || fail "the summary is wrong: $summary"
LOCAL_NAME=$(echo "$summary" | jq -r .name)
ok "the summary of $LOCAL_NAME: $(echo "$summary" | jq -r '"\(.nodes) nodes, \(.monthlyCost | floor) \(.currency) a month"')"

log "This installation reads itself as another cluster, and says why it cannot reach a third"
MIRROR_URL="http://$RELEASE-api.$NS.svc:8082"
track_call PUT /api/settings '{"clusters":[]}'
TEST_BODY='{"name":"mirror","url":"'"$MIRROR_URL"'","token":"'"$HUB_TOKEN"'"}'
api POST /api/settings/providers/cluster/test "$TEST_BODY" | jq -e '.connected' >/dev/null || fail "testing the cluster: $(api POST /api/settings/providers/cluster/test "$TEST_BODY")"
ok "Test connection reaches it before saving"
CLUSTERS_BODY='{"clusters":[{"name":"mirror","url":"'"$MIRROR_URL"'","token":"'"$HUB_TOKEN"'"},{"name":"nowhere","url":"http://nowhere.smoke-hub.svc:8082","token":"cdk_none"}]}'
api PUT /api/settings "$CLUSTERS_BODY" >/dev/null || fail "saving the clusters"
api GET /api/settings | grep -q "$HUB_TOKEN" && fail "the token came back from the settings"
wait_for "the mirror reports the same nodes as this installation" mirrored
row nowhere | jq -e '.error | test("could not reach")' >/dev/null || fail "the unreachable cluster: $(row nowhere)"
clusters | jq -e '(.clusters | length) == 3 and .clusters[0].local and ([.totals[].clusters] | add) == 2' >/dev/null ||
  fail "the list is wrong: $(clusters | jq -c '{clusters: [.clusters[] | {name, local, error}], totals}')"
ok "three rows, this one first, totals over the two that answered"
