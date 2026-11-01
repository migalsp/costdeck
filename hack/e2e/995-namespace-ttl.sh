# shellcheck shell=bash
# Preview cleanup deletes the namespaces a label picks once they are older than their
# time to live. A dry run deletes and sends nothing; then an expired namespace that was
# warned about goes at once, one that expired unwarned is warned about and kept for the
# warning lead, one about to expire is warned about first, an extended one stays, and a
# kept namespace, one a schedule lists by name and one without the label are never touched.
# The suite installs the chart with namespaceTTL.enabled.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it deletes namespaces and changes the cleanup and notification settings"
  return 0
fi
if [[ "$(kubectl auth can-i delete namespaces --as="system:serviceaccount:$NS:$RELEASE")" != yes ]]; then
  [[ -n "${SKIP_INSTALL:-}" ]] || fail "installed with namespaceTTL.enabled, yet the operator cannot delete namespaces"
  echo "skip: the release was installed without namespaceTTL.enabled"
  return 0
fi

TTL_LABEL=costdeck-e2e-ttl
in_seconds() { python3 -c "import datetime,sys; print((datetime.datetime.utcnow() + datetime.timedelta(seconds=int(sys.argv[1]))).strftime('%Y-%m-%dT%H:%M:%SZ'))" "$1"; }
ttl_namespace() { # name, then extra label and annotation lines
  track_namespace "$1"
  kubectl apply -f - >/dev/null <<YAML || fail "creating $1"
apiVersion: v1
kind: Namespace
metadata:
  name: $1
  labels: {$2}
  annotations: {$3}
YAML
}
gone() { [[ "$(kubectl get namespace "$1" -o jsonpath='{.status.phase}' 2>/dev/null)" =~ ^(Terminating)?$ ]]; }
present() { [[ "$(kubectl get namespace "$1" -o jsonpath='{.status.phase}' 2>/dev/null)" == Active ]]; }
plan() { api GET /api/namespace-ttl; }

log "Preview namespaces with different fates, and a channel for the warnings"
start_webhook_receiver
track_call PUT /api/settings '{"namespaceTTL":{"enabled":false},"integrations":{"notifications":{"slack":{"enabled":false}}}}'
TTL_SLACK='{"integrations":{"notifications":{"slack":{"enabled":true,"webhookUrl":"'"$HOOK_URL"'?ttl","events":["alerts"]}}}}'
api PUT /api/settings "$TTL_SLACK" >/dev/null || fail "setting up the alert channel"
EXPIRED_AT=$(in_seconds -60)
ttl_namespace smoke-ttl-expired "$TTL_LABEL: \"yes\"" "costdeck.io/expires-at: \"$EXPIRED_AT\", costdeck.io/ttl-warned: \"$EXPIRED_AT\""
ttl_namespace smoke-ttl-unwarned "$TTL_LABEL: \"yes\"" "costdeck.io/expires-at: \"$(in_seconds -60)\""
ttl_namespace smoke-ttl-soon "$TTL_LABEL: \"yes\"" "costdeck.io/expires-at: \"$(in_seconds 150)\""
ttl_namespace smoke-ttl-extend "$TTL_LABEL: \"yes\"" "costdeck.io/expires-at: \"$(in_seconds 150)\""
ttl_namespace smoke-ttl-kept "$TTL_LABEL: \"yes\", costdeck.io/ttl: \"off\"" ""
ttl_namespace smoke-ttl-named "$TTL_LABEL: \"yes\"" "costdeck.io/expires-at: \"$(in_seconds -60)\""
ttl_namespace smoke-ttl-other "" "costdeck.io/expires-at: \"$(in_seconds -60)\""
TTL_GROUP='{"metadata":{"name":"smoke-ttl"},"spec":{"category":"e2e","namespaces":["smoke-ttl-named"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
track_group smoke-ttl
expect_code 201 "a schedule that lists smoke-ttl-named" POST /api/scaling/groups "$TTL_GROUP" -b "$JAR"

log "A dry run plans, and deletes and sends nothing"
TTL_DRY='{"namespaceTTL":{"enabled":true,"dryRun":true,"selector":"'"$TTL_LABEL"'=yes","ttl":"1d","warnBefore":"1h"}}'
api PUT /api/settings "$TTL_DRY" >/dev/null || fail "turning on the dry run"
p=$(plan)
echo "$p" | jq -e '.permitted and .dryRun and ([.namespaces[] | select(.protected == null) | .namespace] | sort == ["smoke-ttl-expired","smoke-ttl-extend","smoke-ttl-soon","smoke-ttl-unwarned"])' >/dev/null ||
  fail "the plan is wrong: $(echo "$p" | jq -c '{permitted, dryRun, namespaces: [.namespaces[] | {namespace, protected}]}')"
echo "$p" | jq -e '[.namespaces[] | select(.protected != null) | .namespace] | sort == ["smoke-ttl-kept","smoke-ttl-named"]' >/dev/null ||
  fail "the protected namespaces are wrong: $(echo "$p" | jq -c '[.namespaces[] | {namespace, protected}]')"
ok "four would go, two are protected, the unlabelled one is not even listed"
holds_for 40 "the expired namespace survives the dry run, and nothing is sent" sh -c \
  "kubectl get namespace smoke-ttl-expired -o jsonpath='{.status.phase}' | grep -qx Active && [ \"\$(kubectl exec -n smoke-hooks deploy/hook -- sh -c 'ls /tmp/ttl-*.json 2>/dev/null | wc -l' | tr -d ' ')\" = 0 ]"

log "Deleting for real: the expired one goes, the next one is warned about, one is kept longer"
TTL_ON='{"namespaceTTL":{"dryRun":false}}'
api PUT /api/settings "$TTL_ON" >/dev/null || fail "turning the dry run off"
wait_for "the expired namespace is deleted" gone smoke-ttl-expired
wait_for "its deletion is announced" hook_has ttl 'Deleted namespace `smoke-ttl-expired`'
wait_for "the namespace about to expire is warned about" hook_has ttl '`smoke-ttl-soon` will be deleted'
wait_for "the namespace that expired unwarned is warned about" hook_has ttl '`smoke-ttl-unwarned` will be deleted'
present smoke-ttl-unwarned || fail "smoke-ttl-unwarned was deleted without a warning"
UNWARNED_UNTIL=$(kubectl get namespace smoke-ttl-unwarned -o jsonpath="{.metadata.annotations['costdeck\.io/expires-at']}")
[[ "$UNWARNED_UNTIL" > "$(in_seconds 3000)" ]] || fail "its deletion was not moved by the one-hour warning lead: $UNWARNED_UNTIL"
ok "smoke-ttl-unwarned gets the warning lead before it goes"
[[ -n "$(kubectl get namespace smoke-ttl-soon -o jsonpath="{.metadata.annotations['costdeck\.io/ttl-warned']}")" ]] || fail "the warning is not recorded on the namespace"
api POST /api/namespace-ttl/smoke-ttl-extend/extend '{"by":"3d"}' | jq -e '.source == "extended"' >/dev/null || fail "extending smoke-ttl-extend"
ok "smoke-ttl-extend kept three more days"
TIMEOUT=240 wait_for "the warned namespace is deleted when its time is up" gone smoke-ttl-soon
for ns in smoke-ttl-extend smoke-ttl-unwarned smoke-ttl-kept smoke-ttl-named smoke-ttl-other; do
  present "$ns" || fail "$ns was deleted"
done
ok "the extended, kept, scheduled and unlabelled namespaces are all still there"
plan | jq -e '[.deleted[].namespace] | index("smoke-ttl-expired") != null and index("smoke-ttl-soon") != null' >/dev/null ||
  fail "the record of deletions is wrong: $(plan | jq -c .deleted)"
ok "both deletions are on record"
