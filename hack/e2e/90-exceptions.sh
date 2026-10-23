# shellcheck shell=bash
# Dated exceptions and holiday calendars override a schedule's weekly hours.
#
# The JSON bodies are built in plain assignments: bash 3.2 (macOS) mangles \" inside
# "$(...)", which then splits the body on its commas.

TODAY=$(date -u +%F)
# GNU date in CI, BSD date on macOS; today and tomorrow keep the test away from midnight.
TOMORROW=$(date -u -d tomorrow +%F 2>/dev/null || date -u -v+1d +%F)
OPEN_SCHEDULE_JSON='[{"days":[0,1,2,3,4,5,6],"startTime":"00:00","endTime":"23:59","timezone":"UTC"}]'
# group_body <schedules> <extra spec fields>: a request body for the smoke-exc group.
group_body() {
  printf '{"metadata":{"name":"smoke-exc"},"spec":{"category":"e2e","namespaces":["smoke-exc"],"schedules":%s%s}}' "$1" "$2"
}
OFFICE_MOVE=",\"exceptions\":[{\"name\":\"Office move\",\"start\":\"$TODAY\",\"end\":\"$TOMORROW\",\"state\":\"Down\"}]"
BACKWARDS=",\"exceptions\":[{\"start\":\"$TOMORROW\",\"end\":\"$TODAY\",\"state\":\"Down\"}]"
RELEASE_WEEKEND=",\"exceptions\":[{\"name\":\"Release\",\"start\":\"$TODAY\",\"end\":\"$TOMORROW\",\"state\":\"Up\"}]"
CALENDARS="{\"calendars\":[{\"name\":\"e2e-holidays\",\"days\":[{\"date\":\"$TODAY\",\"name\":\"E2E day\"},{\"date\":\"$TOMORROW\",\"name\":\"E2E day\"}]}]}"

log "An exception keeps a namespace down on its dates, whatever the hours say"
new_namespace smoke-exc
deployment smoke-exc app 2
kubectl rollout status deploy/app -n smoke-exc --timeout=120s >/dev/null
track_group smoke-exc
body=$(group_body "$OPEN_SCHEDULE_JSON" "$OFFICE_MOVE")
expect_code 201 "a schedule open all week, with a Down exception for today" POST /api/scaling/groups "$body" -b "$JAR"
wait_for "the exception scales the namespace to zero" replicas_are smoke-exc app 0
wait_for "status says an exception is in control" group_field_is smoke-exc mode Exception
group_field_is smoke-exc exception "Office move" || fail "status.exception does not name the exception"
ok "status names the exception"

log "A manual start still beats the exception"
api POST /api/scaling/groups/smoke-exc/manual '{"active":true,"until":"1h"}' >/dev/null || fail "manual override"
wait_for "Start now brings it back during the exception" replicas_are smoke-exc app 2
api POST /api/scaling/groups/smoke-exc/manual '{"active":null}' >/dev/null || fail "resuming the schedule"
wait_for "resuming hands control back to the exception" replicas_are smoke-exc app 0

log "Exceptions that would not apply are refused when saving"
body=$(group_body "$OPEN_SCHEDULE_JSON" "$BACKWARDS")
expect_code 400 "an exception that ends before it starts" PUT /api/scaling/groups/smoke-exc "$body" -b "$JAR"
body=$(group_body "$OPEN_SCHEDULE_JSON" ',"calendars":["no-such-calendar"]')
expect_code 400 "a calendar that does not exist" PUT /api/scaling/groups/smoke-exc "$body" -b "$JAR"

log "Without the exception the hours decide again"
body=$(group_body "$OPEN_SCHEDULE_JSON" '')
expect_code 200 "exception removed" PUT /api/scaling/groups/smoke-exc "$body" -b "$JAR"
wait_for "the namespace comes back" replicas_are smoke-exc app 2
wait_for "the schedule is in control" group_field_is smoke-exc mode Schedule

log "An Up exception keeps a closed schedule up"
body=$(group_body "$CLOSED_SCHEDULE_JSON" "$RELEASE_WEEKEND")
expect_code 200 "closed hours with an Up exception for today" PUT /api/scaling/groups/smoke-exc "$body" -b "$JAR"
wait_for "status says the release keeps it up" group_field_is smoke-exc exception Release
holds_for 10 "the namespace stays up" replicas_are smoke-exc app 2

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: holiday calendars change the settings of the installed release"
  return 0
fi

log "A holiday calendar applies its days to the schedules that name it"
track_call PUT /api/settings '{"calendars":[]}'
expect_code 200 "a calendar with today and tomorrow" PUT /api/settings "$CALENDARS" -b "$JAR"
body=$(group_body "$OPEN_SCHEDULE_JSON" ',"calendars":["e2e-holidays"]')
expect_code 200 "the schedule names the calendar" PUT /api/scaling/groups/smoke-exc "$body" -b "$JAR"
wait_for "the calendar day scales the namespace to zero" replicas_are smoke-exc app 0
wait_for "status names the calendar day" group_field_is smoke-exc exception "E2E day"
expect_code 200 "the days are taken out of the calendar" PUT /api/settings '{"calendars":[{"name":"e2e-holidays","days":[]}]}' -b "$JAR"
# A settled group is requeued only every minute; the calendar change itself must trigger it.
TIMEOUT=25 wait_for "a changed calendar applies at once" replicas_are smoke-exc app 2
