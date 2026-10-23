# shellcheck shell=bash
# Acting on right-sizing advice against real pods, metrics-server and a real Gitea:
# - applying advice patches a workload and is recorded;
# - a workload whose new pods fail with the smaller request (its container reads its CPU
#   request through the downward API and exits below 100m) gets its old requests back
#   from the guard, which tells the alert channel;
# - a workload Argo CD manages is refused;
# - a pull request with the advice is opened in a Gitea repository holding the manifest.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it changes workloads, the right-sizing settings and installs Gitea"
  return 0
fi

GITEA_IMAGE=${GITEA_IMAGE:-gitea/gitea:1.27.3-rootless}
GITEA_PORT=${GITEA_PORT:-13000}
rs_workload() { # name, extra annotations, the container's script
  kubectl apply -f - >/dev/null <<YAML || fail "applying $1"
apiVersion: apps/v1
kind: Deployment
metadata: {name: $1, namespace: smoke-rs, annotations: {$2}}
spec:
  replicas: 1
  selector: {matchLabels: {app: $1}}
  template:
    metadata: {labels: {app: $1}}
    spec:
      terminationGracePeriodSeconds: 1
      containers:
        - name: main
          image: $BUSYBOX_IMAGE
          command: [sh, -c, '$3']
          env: [{name: CPU_MILLIS, valueFrom: {resourceFieldRef: {containerName: main, resource: requests.cpu, divisor: 1m}}}]
          resources: {requests: {cpu: 500m, memory: 256Mi}}
YAML
}
advice_ready() { api GET /api/namespaces/smoke-rs/recommendations | jq -e --arg n "$1" '.workloads[] | select(.name == $n) | .containers[0].cpu.action == "reduce"' >/dev/null; }
cpu_request() { kubectl get deploy "$1" -n smoke-rs -o jsonpath='{.spec.template.spec.containers[0].resources.requests.cpu}'; }
change_state() { api GET /api/rightsizing/changes?namespace=smoke-rs | jq -r --arg n "$1" '[.changes[] | select(.name == $n)][0].state // empty'; }
state_is() { [[ "$(change_state "$1")" == "$2" ]]; }
rs_post() { api_code POST "/api/namespaces/smoke-rs/rightsizing/$1" '{"kind":"Deployment","name":"'"$2"'"}' -b "$JAR"; }
rs_why() { api POST "/api/namespaces/smoke-rs/rightsizing/$1" '{"kind":"Deployment","name":"'"$2"'"}'; }

log "Workloads that request ten times what they use"
# Earlier scenarios leave their seeded VictoriaMetrics as the usage source until the suite
# ends, and it holds no samples of these pods: read metrics-server.
api PUT /api/settings '{"integrations":{"victoriaMetrics":{"enabled":false}}}' >/dev/null || fail "switching usage to metrics-server"
new_namespace smoke-rs
rs_workload steady "" 'sleep 3600'
rs_workload crashy "" 'if [ "$CPU_MILLIS" -lt 100 ]; then echo "too little CPU: ${CPU_MILLIS}m"; sleep 3; exit 1; fi; sleep 3600'
rs_workload proposed "" 'sleep 3600'
rs_workload managed 'argocd.argoproj.io/tracking-id: "shop:apps/Deployment:smoke-rs/managed"' 'sleep 3600'
for d in steady crashy proposed managed; do wait_for "$d is up" ready_are smoke-rs "$d" 1; done
# metrics-server reports a pod's usage a minute or so after it starts.
for d in steady crashy proposed managed; do TIMEOUT=240 wait_for "advice to reduce $d" advice_ready "$d"; done

log "Applying is off until an admin turns it on"
[[ "$(rs_post apply steady)" == 403 ]] || fail "applying while it is off was not refused"
start_webhook_receiver
track_call PUT /api/settings '{"rightsizing":{"apply":false,"removeGit":true},"integrations":{"notifications":{"slack":{"enabled":false}}}}'
RS_ON='{"rightsizing":{"apply":true,"guardMinutes":5},"integrations":{"notifications":{"slack":{"enabled":true,"webhookUrl":"'"$HOOK_URL"'?rs","events":["alerts"]}}}}'
api PUT /api/settings "$RS_ON" >/dev/null || fail "turning applying on"
ok "refused, then turned on with a watch of 5 minutes"

log "Applied advice is recorded; a workload Argo CD manages is refused"
[[ "$(rs_post apply steady)" == 200 ]] || fail "applying the advice to steady: $(rs_why apply steady)"
[[ "$(cpu_request steady)" =~ ^[0-9]+m$ ]] && [[ "$(cpu_request steady)" != 500m ]] || fail "steady still requests $(cpu_request steady)"
state_is steady applied || fail "steady is $(change_state steady)"
ok "steady now requests $(cpu_request steady), watched"
[[ "$(rs_post apply managed)" == 409 ]] || fail "a workload Argo CD manages was not refused"
[[ "$(cpu_request managed)" == 500m ]] || fail "the managed workload was changed"
ok "the Argo CD workload is left to a pull request"

log "New pods that fail with the smaller request get the old one back"
[[ "$(rs_post apply crashy)" == 200 ]] || fail "applying the advice to crashy: $(rs_why apply crashy)"
TIMEOUT=240 wait_for "the guard puts crashy's request back" state_is crashy reverted
[[ "$(cpu_request crashy)" == 500m ]] || fail "crashy requests $(cpu_request crashy)"
api GET /api/rightsizing/changes?namespace=smoke-rs | jq -e '[.changes[] | select(.name == "crashy")][0].reason | test("restarted|crash-looping")' >/dev/null ||
  fail "the reason is: $(api GET /api/rightsizing/changes?namespace=smoke-rs | jq -c '[.changes[] | select(.name == "crashy")][0]')"
wait_for "the alert channel is told" hook_has rs 'Put back the requests of Deployment `smoke-rs/crashy`'
wait_for "crashy runs again" ready_are smoke-rs crashy 1

log "Putting a change back by hand"
[[ "$(rs_post revert steady)" == 200 ]] || fail "putting steady back"
[[ "$(cpu_request steady)" == 500m ]] || fail "steady requests $(cpu_request steady)"
state_is steady reverted || fail "steady is $(change_state steady)"
ok "steady requests 500m again"

log "A pull request with the advice in a real Gitea"
new_namespace smoke-git
kubectl apply -f - >/dev/null <<YAML || fail "applying Gitea"
apiVersion: apps/v1
kind: Deployment
metadata: {name: gitea, namespace: smoke-git}
spec:
  selector: {matchLabels: {app: gitea}}
  template:
    metadata: {labels: {app: gitea}}
    spec:
      containers:
        - name: gitea
          image: $GITEA_IMAGE
          env:
            - {name: GITEA__security__INSTALL_LOCK, value: "true"}
            - {name: GITEA__database__DB_TYPE, value: sqlite3}
            - {name: GITEA__server__ROOT_URL, value: "http://gitea.smoke-git.svc:3000/"}
          readinessProbe: {httpGet: {path: /api/healthz, port: 3000}, periodSeconds: 3}
---
apiVersion: v1
kind: Service
metadata: {name: gitea, namespace: smoke-git}
spec: {selector: {app: gitea}, ports: [{port: 3000}]}
YAML
kubectl rollout status deploy/gitea -n smoke-git --timeout=240s >/dev/null || fail "Gitea did not start"
kubectl exec -n smoke-git deploy/gitea -- gitea admin user create --admin --username costdeck --password costdeck-e2e-pw \
  --email costdeck@e2e.test --must-change-password=false >/dev/null || fail "creating the Gitea user"
kubectl port-forward -n smoke-git svc/gitea "$GITEA_PORT:3000" >/dev/null 2>&1 &
PF_PIDS="$PF_PIDS $!"
wait_for "Gitea's API answers" curl -sf "localhost:$GITEA_PORT/api/v1/version"
gitea() { curl -sf -H "Authorization: token $GITEA_TOKEN" -H 'Content-Type: application/json' "$@"; }
GITEA_TOKEN=$(curl -sf -u costdeck:costdeck-e2e-pw -H 'Content-Type: application/json' -X POST "localhost:$GITEA_PORT/api/v1/users/costdeck/tokens" \
  -d '{"name":"e2e","scopes":["write:repository","write:user"]}' | jq -r .sha1)
[[ -n "$GITEA_TOKEN" && "$GITEA_TOKEN" != null ]] || fail "no Gitea token"
gitea -X POST "localhost:$GITEA_PORT/api/v1/user/repos" -d '{"name":"apps","auto_init":true,"default_branch":"main"}' >/dev/null || fail "creating the repository"
MANIFEST="apiVersion: apps/v1
kind: Deployment
metadata:
  name: proposed
  namespace: smoke-rs # the team's API
spec:
  template:
    spec:
      containers:
        - name: main
          resources:
            requests:
              cpu: 500m # sized for the launch
              memory: 256Mi"
ADD_FILE=$(jq -n --arg c "$(printf '%s\n' "$MANIFEST" | base64 | tr -d '\n')" '{content: $c, message: "Add the API"}')
gitea -X POST "localhost:$GITEA_PORT/api/v1/repos/costdeck/apps/contents/deploy/proposed.yaml" -d "$ADD_FILE" >/dev/null || fail "adding the manifest"
ok "a repository with the workload's manifest"

GIT_SETTINGS='{"provider":"gitea","repository":"costdeck/apps","apiUrl":"http://gitea.smoke-git.svc:3000/api/v1","path":"deploy","token":"'"$GITEA_TOKEN"'"}'
api POST /api/settings/providers/git/test "$GIT_SETTINGS" | jq -e .connected >/dev/null || fail "testing the repository: $(api POST /api/settings/providers/git/test "$GIT_SETTINGS")"
RS_GIT='{"rightsizing":{"git":'"$GIT_SETTINGS"'}}'
api PUT /api/settings "$RS_GIT" >/dev/null || fail "saving the repository"
pr=$(api POST /api/namespaces/smoke-rs/rightsizing/pull-request '{"kind":"Deployment","name":"proposed"}') || fail "opening the pull request: $pr"
echo "$pr" | jq -e '.state == "proposed" and (.pullRequest | test("/costdeck/apps/pulls/"))' >/dev/null || fail "the proposal: $pr"
[[ "$(cpu_request proposed)" == 500m ]] || fail "a pull request changed the workload itself"
pulls=$(gitea "localhost:$GITEA_PORT/api/v1/repos/costdeck/apps/pulls?state=open")
echo "$pulls" | jq -e 'length == 1 and .[0].title == "Right-size Deployment smoke-rs/proposed" and (.[0].body | test("Proposed by Cost Deck"))' >/dev/null ||
  fail "the pull requests in Gitea: $(echo "$pulls" | jq -c '[.[] | {title}]')"
diff=$(gitea "localhost:$GITEA_PORT/api/v1/repos/costdeck/apps/pulls/$(echo "$pulls" | jq -r '.[0].number').diff")
echo "$diff" | grep -q '^+ *cpu: [0-9]*m # sized for the launch' || fail "the diff does not set the CPU request: $diff"
# The diff shows three lines around the change; the whole file keeps every comment.
branch=$(echo "$pulls" | jq -r '.[0].head.ref')
proposed_file=$(gitea "localhost:$GITEA_PORT/api/v1/repos/costdeck/apps/contents/deploy/proposed.yaml?ref=$branch" | jq -r .content | base64 --decode)
echo "$proposed_file" | grep -q 'namespace: smoke-rs # the team' || fail "the comment was lost: $proposed_file"
ok "the pull request sets the requests and keeps the comments"
