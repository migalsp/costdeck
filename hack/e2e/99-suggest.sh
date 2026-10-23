# shellcheck shell=bash
# A schedule is suggested from a week of CPU usage in a real VictoriaMetrics: a namespace
# busy 09:00-17:00 UTC on weekdays gets Monday to Friday, 08:00-18:00, an hour of margin
# on either side.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it changes the VictoriaMetrics settings of the installed release"
  return 0
fi

suggestion() { api GET "/api/namespaces/smoke-suggest/schedule-suggestion?timezone=UTC"; }
suggests_working_hours() {
  suggestion | jq -e '.reason == "pattern" and .schedules == [{"days": [1, 2, 3, 4, 5], "startTime": "08:00", "endTime": "18:00", "timezone": "UTC"}]' >/dev/null
}

log "Eight days of usage, busy on weekdays from 09:00 to 17:00 UTC"
new_namespace smoke-suggest
deployment smoke-suggest api 1
start_victoriametrics 7
python3 - <<'PY' | curl -sf --data-binary @- "localhost:$VM_PORT/api/v1/import" || fail "importing the usage history"
import json, time
now = int(time.time()) // 60 * 60 - 120
start, step = now - 8 * 86400, 120
busy = lambda t: time.gmtime(t).tm_wday < 5 and 9 <= time.gmtime(t).tm_hour < 17
ts, values, total = [], [], 0.0
for t in range(start, now + 1, step):
    total += (0.5 if busy(t) else 0.001) * step
    ts.append(t * 1000)
    values.append(round(total, 3))
labels = {"__name__": "container_cpu_usage_seconds_total", "namespace": "smoke-suggest", "pod": "api-1", "container": "api"}
print(json.dumps({"metric": labels, "values": values, "timestamps": ts}))
PY
curl -sf "localhost:$VM_PORT/internal/force_flush" >/dev/null
# Usage history is cached for two minutes.
TIMEOUT=150 wait_for "the suggestion is Monday to Friday, 08:00-18:00" suggests_working_hours
s=$(suggestion)
echo "$s" | jq -e '.source == "victoriametrics" and .days >= 6 and (.upShare * 168 | round) == 50' >/dev/null ||
  fail "the suggestion is wrong: $(echo "$s" | jq -c '{source, days, upShare}')"
ok "up 50 hours of the week, from VictoriaMetrics"
saving_priced() { suggestion | jq -e '.monthlySaving > 0' >/dev/null; }
# The saving needs the namespace's first usage sample, taken within a minute or two.
TIMEOUT=150 wait_for "the suggestion prices what it would save" saving_priced
