# shellcheck shell=bash
# Notifications to Slack, Microsoft Teams and email. A webhook receiver (busybox httpd
# with a CGI script that keeps every request) stands in for Slack and Teams, and Mailpit
# for the SMTP server. Each channel only gets the kinds of message it asked for.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it changes the notification settings of the installed release"
  return 0
fi

MAILPIT_IMAGE=${MAILPIT_IMAGE:-axllent/mailpit:v1.30.7}
MAILPIT_PORT=${MAILPIT_PORT:-18025}
HOOK_URL="http://hook.smoke-notify.svc:8080/cgi-bin/hook"
hook_requests() { kubectl exec -n smoke-notify deploy/hook -- sh -c "cat /tmp/$1-*.json 2>/dev/null"; }
hook_has() { hook_requests "$1" | grep -q "$2"; }
hook_count() { kubectl exec -n smoke-notify deploy/hook -- sh -c "ls /tmp/$1-*.json 2>/dev/null | wc -l" | tr -d ' '; }
mail_subjects() { curl -sf "localhost:$MAILPIT_PORT/api/v1/messages" | jq -r '.messages[].Subject'; }
mail_has() { mail_subjects | grep -q "$1"; }

log "A webhook receiver and an SMTP server stand in for Slack, Teams and email"
new_namespace smoke-notify
kubectl apply -f - >/dev/null <<YAML || fail "applying the receivers"
apiVersion: v1
kind: ConfigMap
metadata: {name: hook, namespace: smoke-notify}
data:
  hook: |
    #!/bin/sh
    head -c "\${CONTENT_LENGTH:-0}" > "/tmp/\${QUERY_STRING:-hook}-\$\$.json"
    printf 'Content-Type: text/plain\r\n\r\nok\n'
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: hook, namespace: smoke-notify}
spec:
  selector: {matchLabels: {app: hook}}
  template:
    metadata: {labels: {app: hook}}
    spec:
      containers:
        - name: httpd
          image: $BUSYBOX_IMAGE
          command: [sh, -c, "mkdir -p /www/cgi-bin && cp /cfg/hook /www/cgi-bin/hook && chmod +x /www/cgi-bin/hook && exec httpd -f -p 8080 -h /www"]
          volumeMounts: [{name: cfg, mountPath: /cfg}]
          readinessProbe: {tcpSocket: {port: 8080}, periodSeconds: 2}
      volumes: [{name: cfg, configMap: {name: hook}}]
---
apiVersion: v1
kind: Service
metadata: {name: hook, namespace: smoke-notify}
spec: {selector: {app: hook}, ports: [{port: 8080}]}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: mailpit, namespace: smoke-notify}
spec:
  selector: {matchLabels: {app: mailpit}}
  template:
    metadata: {labels: {app: mailpit}}
    spec:
      containers:
        - name: mailpit
          image: $MAILPIT_IMAGE
          readinessProbe: {tcpSocket: {port: 1025}, periodSeconds: 2}
---
apiVersion: v1
kind: Service
metadata: {name: mailpit, namespace: smoke-notify}
spec: {selector: {app: mailpit}, ports: [{name: smtp, port: 1025}, {name: http, port: 8025}]}
YAML
kubectl rollout status deploy/hook -n smoke-notify --timeout=120s >/dev/null || fail "the webhook receiver did not start"
kubectl rollout status deploy/mailpit -n smoke-notify --timeout=180s >/dev/null || fail "Mailpit did not start"
kubectl port-forward -n smoke-notify svc/mailpit "$MAILPIT_PORT:8025" >/dev/null 2>&1 &
PF_PIDS="$PF_PIDS $!"
wait_for "Mailpit's API answers" curl -sf "localhost:$MAILPIT_PORT/api/v1/messages"

log "Channels set up through the settings API, each for its own kinds of message"
track_call PUT /api/settings '{"integrations":{"notifications":{"slack":{"enabled":false},"teams":{"enabled":false},"email":{"enabled":false}}}}'
NOTIFY_BODY='{"integrations":{"notifications":{
  "slack":{"enabled":true,"webhookUrl":"'"$HOOK_URL"'?slack","events":["schedules"]},
  "teams":{"enabled":true,"webhookUrl":"'"$HOOK_URL"'?teams","events":["digests"]},
  "email":{"enabled":true,"host":"mailpit.smoke-notify.svc","port":1025,"security":"none","from":"Cost Deck <costdeck@smoke.test>","to":["finops@smoke.test"],"events":["schedules","digests"]}}}}'
api PUT /api/settings "$NOTIFY_BODY" >/dev/null || fail "saving the channels"
settings=$(api GET /api/settings)
echo "$settings" | jq -e '.integrations.notifications | .slack.hasWebhook and .teams.hasWebhook and .email.to == ["finops@smoke.test"] and .slack.events == ["schedules"]' >/dev/null ||
  fail "the channels did not come back: $(echo "$settings" | jq -c .integrations.notifications)"
if echo "$settings" | grep -q 'cgi-bin/hook'; then fail "a webhook URL came back from the API"; fi
ok "saved, with the webhook URLs kept in Secrets"

log "Test messages reach a webhook and a mailbox"
api POST /api/settings/providers/slack/test '{}' | jq -e '.connected' >/dev/null || fail "the Slack test failed"
wait_for "the Slack test message arrived" hook_has slack 'Cost Deck test message'
api POST /api/settings/providers/email/test '{}' | jq -e '.connected' >/dev/null || fail "the email test failed: $(api POST /api/settings/providers/email/test '{}')"
wait_for "the test email arrived" mail_has 'Cost Deck test message'

log "A schedule that scales down is announced on Slack and by email, not on Teams"
new_namespace smoke-notify-app
deployment smoke-notify-app web 1
wait_for "the workload is up" ready_are smoke-notify-app web 1
NOTIFY_GROUP='{"metadata":{"name":"smoke-notify"},"spec":{"category":"e2e","namespaces":["smoke-notify-app"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
track_group smoke-notify
expect_code 201 "a closed schedule" POST /api/scaling/groups "$NOTIFY_GROUP" -b "$JAR"
wait_for "the namespace is scaled down" replicas_are smoke-notify-app web 0
wait_for "Slack has the scaling change" hook_has slack 'smoke-notify.*is scaled down'
wait_for "the email has it" mail_has 'smoke-notify.*is scaled down'
[[ "$(hook_count teams)" == 0 ]] || fail "Teams received a scaling change it did not ask for"
ok "Teams, set to digests only, got nothing"

log "The digest goes to Teams and email"
api GET /api/reports | jq -e '.channels == ["Teams","email"]' >/dev/null || fail "the reports page names the wrong channels: $(api GET /api/reports | jq -c .channels)"
entry=$(api POST /api/reports/digest/send '{"period":"week"}') || fail "sending the digest: $entry"
echo "$entry" | jq -e '.delivered == "Teams, email"' >/dev/null || fail "the digest went to: $(echo "$entry" | jq -r .delivered)"
wait_for "Teams has the digest" hook_has teams 'Cost digest'
hook_has teams '"type":"AdaptiveCard"' || fail "the Teams message is not an Adaptive Card: $(hook_requests teams | head -c 300)"
wait_for "the digest email arrived" mail_has 'Cost Deck: Cost digest'
[[ "$(hook_count slack)" -le 2 ]] || fail "Slack received the digest it did not ask for"
ok "every channel got exactly what it asked for"
