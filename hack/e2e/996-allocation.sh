# shellcheck shell=bash
# Cost allocation from the real cost history: a day with known amounts is written into the
# history ConfigMap (the collector keeps it, since it reads the history before each
# sample), the policy is set through the settings API, and the report and its CSV charge
# the shared namespace and the idle capacity to the teams, adding up to the bill.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it writes into the cost history and changes the allocation settings"
  return 0
fi

ALLOC_DAY=$(python3 -c 'import datetime; print((datetime.datetime.utcnow() - datetime.timedelta(days=10)).strftime("%Y-%m-%d"))')
allocation() { api GET "/api/finops/allocation?by=$1&from=$ALLOC_DAY&to=$ALLOC_DAY"; }
team_total() { allocation team | jq -e --arg t "$1" --argjson v "$2" '.rows[] | select(.key == $t) | .total == $v' >/dev/null; }

log "Two teams, a shared namespace and a day of known cost in the history"
new_namespace smoke-alloc-a
new_namespace smoke-alloc-b
new_namespace smoke-alloc-shared
kubectl label namespace smoke-alloc-a team=smoke-a cost-center=cc-7 --overwrite >/dev/null
kubectl label namespace smoke-alloc-b team=smoke-b --overwrite >/dev/null
# 100 of nodes, 10 of volumes, 5 of load balancers; pods request 60, so 40 is idle.
seed_day() {
  kubectl get configmap costdeck-cost-history -n "$NS" -o json | python3 -c '
import json, sys
day = sys.argv[1]
cm = json.load(sys.stdin)
history = json.loads(cm["data"]["history.json"])
history["days"] = [d for d in history.get("days") or [] if d["date"] != day]
history["days"].append({"date": day, "hours": 24, "provisioned": 100, "requested": 60, "used": 30, "saved": 0, "storage": 10, "network": 5,
  "ns": {"smoke-alloc-a": {"c": 30, "u": 15, "s": 6, "n": 5}, "smoke-alloc-b": {"c": 20, "u": 10, "s": 4}, "smoke-alloc-shared": {"c": 10, "u": 5}}})
history["days"].sort(key=lambda d: d["date"])
cm["data"]["history.json"] = json.dumps(history)
json.dump(cm, sys.stdout)' "$ALLOC_DAY" | kubectl replace -f - >/dev/null
}
# The collector may write in between: a replace with a stale resourceVersion is refused.
wait_for "the cost history has the collector's first sample" kubectl get configmap costdeck-cost-history -n "$NS"
wait_for "a day of known cost is written into the history" seed_day

log "The policy shares the namespace and the idle capacity out by requests"
track_call PUT /api/settings '{"allocation":{"idle":"separate","sharedNamespaces":[],"sharedSplit":"proportional"}}'
ALLOC_POLICY='{"allocation":{"idle":"proportional","sharedNamespaces":["smoke-alloc-shared"],"sharedSplit":"proportional"}}'
api PUT /api/settings "$ALLOC_POLICY" >/dev/null || fail "setting the allocation policy"
# The dashboard reads the history at most once a minute.
TIMEOUT=150 wait_for "smoke-a is charged its own cost, 60% of the shared one and of the idle capacity" team_total smoke-a 71
team_total smoke-b 44 || fail "smoke-b: $(allocation team | jq -c '.rows')"
report=$(allocation team)
echo "$report" | jq -e '.total == 115 and .unallocated == 0 and .days == 1 and .sharedNamespaces == ["smoke-alloc-shared"] and ([.rows[].total] | add) == .total' >/dev/null ||
  fail "the report does not add up to the bill: $(echo "$report" | jq -c '{total, unallocated, days, sharedNamespaces, rows: [.rows[] | {key, total}]}')"
ok "71 + 44 = 115, the whole bill"
allocation label:cost-center | jq -e '[.rows[] | {key, total}] == [{"key":"cc-7","total":71},{"key":"(no cost-center)","total":44}]' >/dev/null ||
  fail "by cost-center: $(allocation label:cost-center | jq -c '.rows')"
ok "grouped by any label"

log "Finance gets the same numbers as CSV"
csv=$(curl -sf -b "$JAR" "localhost:$API_PORT/api/finops/allocation.csv?by=team&from=$ALLOC_DAY&to=$ALLOC_DAY") || fail "downloading the CSV"
echo "$csv" | grep -q '^smoke-a,smoke-alloc-a,30.00,6.00,5.00,6.00,24.00,71.00' || fail "the CSV row for smoke-a is wrong: $csv"
echo "$csv" | grep -q '^(total),,,,,,,115.00' || fail "the CSV has no total: $csv"
ok "the CSV has every part of the cost and the total"

log "Idle capacity on a line of its own"
api PUT /api/settings '{"allocation":{"idle":"separate"}}' >/dev/null || fail "changing the idle policy"
idle_apart() { allocation team | jq -e '.unallocated == 40 and .total == 115' >/dev/null; }
wait_for "the 40 of idle capacity is its own line" idle_apart
