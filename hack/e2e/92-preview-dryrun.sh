# shellcheck shell=bash
# The preview says what a schedule would do; a dry run decides but scales nothing until it
# is switched off.

PREVIEW_BODY='{"kind":"group","spec":{"namespaces":["smoke-dry","smoke-nowhere"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
DRY_BODY='{"metadata":{"name":"smoke-dry"},"spec":{"category":"e2e","namespaces":["smoke-dry"],"dryRun":true,"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
dry_message() { kubectl get scalinggroup smoke-dry -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="DryRun")].message}'; }
no_dry_condition() { [[ -z "$(dry_message)" ]]; }

log "The preview lists what a schedule would stop and what that saves, changing nothing"
new_namespace smoke-dry
deployment smoke-dry app 2
kubectl rollout status deploy/app -n smoke-dry --timeout=120s >/dev/null
preview=$(api POST /api/scaling/preview "$PREVIEW_BODY")
echo "$preview" | jq -e '.namespaces[] | select(.name == "smoke-dry") | .workloads[] | select(.name == "app" and .replicas == 2)' >/dev/null ||
  fail "the preview does not list smoke-dry/app with 2 replicas: $preview"
echo "$preview" | jq -e '.savings.hourly > 0 and .savings.monthly > 0 and .upShare < 0.01' >/dev/null ||
  fail "the preview shows no saving: $(echo "$preview" | jq -c '{savings, upShare}')"
echo "$preview" | jq -e '.warnings | any(test("no namespace smoke-nowhere"))' >/dev/null ||
  fail "the preview does not warn about a missing namespace: $(echo "$preview" | jq -c .warnings)"
ok "workloads, saving and warnings"
holds_for 4 "the preview scaled nothing" replicas_are smoke-dry app 2

log "A dry run decides but scales nothing"
track_group smoke-dry
expect_code 201 "a closed schedule in dry run" POST /api/scaling/groups "$DRY_BODY" -b "$JAR"
wait_for "phase DryRun" group_field_is smoke-dry phase DryRun
group_field_is smoke-dry desiredState Down || fail "the dry run does not decide down"
[[ "$(dry_message)" == "Would scale down 1 workload(s) in 1 namespace(s) now."* ]] || fail "DryRun condition says: $(dry_message)"
ok "the DryRun condition says what would change"
holds_for 10 "nothing is scaled" replicas_are smoke-dry app 2

log "Switching the dry run off lets the schedule act"
kubectl patch scalinggroup smoke-dry -n "$NS" --type merge -p '{"spec":{"dryRun":false}}' >/dev/null
wait_for "the namespace is scaled to zero" replicas_are smoke-dry app 0
wait_for "the DryRun condition is gone" no_dry_condition
