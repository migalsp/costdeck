# shellcheck shell=bash
# Cost estimates: the API prices a change of manifests at this cluster's rates and with
# its schedules, and "manager estimate" in the real image, run as a pod, reads the same
# from the API with a viewer token.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it creates an API token and a schedule"
  return 0
fi

EST_BASE='apiVersion: apps/v1
kind: Deployment
metadata: {name: api, namespace: smoke-estimate}
spec:
  replicas: 1
  selector: {matchLabels: {app: api}}
  template:
    metadata: {labels: {app: api}}
    spec:
      containers: [{name: main, image: api, resources: {requests: {cpu: "1", memory: 2Gi}}}]'
EST_HEAD="${EST_BASE/replicas: 1/replicas: 3}
---
apiVersion: v1
kind: Service
metadata: {name: shop, namespace: smoke-estimate}
spec: {type: LoadBalancer, ports: [{port: 80}]}"

log "The API prices a change, counting the namespace's schedule"
new_namespace smoke-estimate
EST_GROUP='{"metadata":{"name":"smoke-estimate"},"spec":{"category":"e2e","namespaces":["smoke-estimate"],"schedules":'"$CLOSED_SCHEDULE_JSON"'}}'
track_group smoke-estimate
expect_code 201 "a schedule that keeps the namespace down" POST /api/scaling/groups "$EST_GROUP" -b "$JAR"
body=$(jq -n --arg base "$EST_BASE" --arg head "$EST_HEAD" '{base: $base, head: $head}')
est=$(api POST /api/estimate "$body") || fail "the estimate: $est"
echo "$est" | jq -e '.items | length == 2' >/dev/null || fail "the estimate lists: $(echo "$est" | jq -c '.items')"
echo "$est" | jq -e '.items[] | select(.kind == "Deployment") | .after.replicas == 3 and (.note | test("up 0% of the time on schedule smoke-estimate"))' >/dev/null ||
  fail "the deployment does not count its schedule: $(echo "$est" | jq -c '.items[] | select(.kind == "Deployment")')"
echo "$est" | jq -e '.markdown | startswith("<!-- costdeck-estimate -->\n### Cost estimate")' >/dev/null || fail "no pull request comment"
ok "two changes priced; the deployment, kept down, adds next to nothing"

log "Negative replicas or requests, which Kubernetes rejects, cannot lower an estimate"
EST_NEGATIVE='apiVersion: apps/v1
kind: Deployment
metadata: {name: offset, namespace: smoke-estimate-unscheduled}
spec:
  replicas: -50
  selector: {matchLabels: {app: offset}}
  template:
    metadata: {labels: {app: offset}}
    spec:
      containers: [{name: main, image: offset, resources: {requests: {cpu: "-4", memory: -8Gi}}}]'
body=$(jq -n --arg head "$EST_NEGATIVE" '{base: "", head: $head}')
EST_NEGATIVE_RESULT=$(api POST /api/estimate "$body") || fail "the estimate: $EST_NEGATIVE_RESULT"
echo "$EST_NEGATIVE_RESULT" | jq -e '.after == 0 and .delta == 0' >/dev/null ||
  fail "negative values changed the estimate: $(echo "$EST_NEGATIVE_RESULT" | jq -c '{after, delta}')"
echo "$EST_NEGATIVE_RESULT" | jq -e '.warnings | any(test("smoke-estimate-unscheduled/offset asks for negative"))' >/dev/null ||
  fail "no warning about the negative values: $(echo "$EST_NEGATIVE_RESULT" | jq -c .warnings)"
ok "they count as zero, with a warning"

log "A Rollout is priced with its replicas"
EST_ROLLOUT='apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata: {name: canary, namespace: smoke-estimate-unscheduled}
spec:
  replicas: 3
  template:
    spec:
      containers: [{name: main, image: canary, resources: {requests: {cpu: "1", memory: 2Gi}}}]'
body=$(jq -n --arg head "$EST_ROLLOUT" '{base: "", head: $head}')
EST_ROLLOUT_RESULT=$(api POST /api/estimate "$body") || fail "the estimate: $EST_ROLLOUT_RESULT"
echo "$EST_ROLLOUT_RESULT" | jq -e '.items | length == 1 and .[0].after.replicas == 3' >/dev/null ||
  fail "the Rollout is not priced with its three replicas: $(echo "$EST_ROLLOUT_RESULT" | jq -c .items)"
ok "its three replicas are counted"

log "The estimate command in the operator image asks the API with a viewer token"
EST_TOKEN=$(api POST /api/tokens '{"name":"smoke-estimate","role":"viewer","expiresInDays":1}' | jq -r .token)
track_call DELETE /api/tokens/smoke-estimate
[[ "$EST_TOKEN" == cdk_* ]] || fail "no API token"
kubectl create secret generic estimate-token -n smoke-estimate --from-literal=COSTDECK_TOKEN="$EST_TOKEN" >/dev/null
kubectl create configmap manifests -n smoke-estimate --from-literal=base.yaml="$EST_BASE" --from-literal=head.yaml="$EST_HEAD" >/dev/null
kubectl apply -f - >/dev/null <<YAML || fail "starting the estimate pod"
apiVersion: v1
kind: Pod
metadata: {name: estimate, namespace: smoke-estimate}
spec:
  restartPolicy: Never
  containers:
    - name: estimate
      image: $IMAGE
      imagePullPolicy: IfNotPresent
      args: [estimate, --base, /in/base.yaml, --head, /in/head.yaml, --server, "http://$RELEASE-api.$NS.svc:8082", --format, json]
      envFrom: [{secretRef: {name: estimate-token}}]
      volumeMounts: [{name: in, mountPath: /in}]
  volumes: [{name: in, configMap: {name: manifests}}]
YAML
estimate_done() { [[ "$(kubectl get pod estimate -n smoke-estimate -o jsonpath='{.status.phase}')" =~ ^(Succeeded|Failed)$ ]]; }
wait_for "the estimate pod finished" estimate_done
[[ "$(kubectl get pod estimate -n smoke-estimate -o jsonpath='{.status.phase}')" == Succeeded ]] || fail "the estimate failed: $(kubectl logs estimate -n smoke-estimate)"
out=$(kubectl logs estimate -n smoke-estimate)
[[ "$(echo "$out" | jq -r .delta)" == "$(echo "$est" | jq -r .delta)" ]] || fail "the command and the API disagree: $(echo "$out" | jq -c '{delta, items: [.items[].kind]}')"
ok "the command in the image gives the API's numbers"
