# shellcheck shell=bash
# Start order across namespaces, and skipOnTimeout: a stage that cannot get ready holds
# back the next one only until its timeout.

log "Stage 2 waits for stage 1, until stage 1 misses its one-minute timeout"
new_namespace smoke-stage-a
new_namespace smoke-stage-b
# Never ready: a readiness probe on a port nothing listens on.
track_group smoke-stages
kubectl apply -f - >/dev/null <<YAML || fail "applying test objects"
apiVersion: apps/v1
kind: Deployment
metadata: {name: stuck, namespace: smoke-stage-a, labels: {costdeck-e2e: "true"}}
spec:
  replicas: 0
  selector: {matchLabels: {app: stuck}}
  template:
    metadata: {labels: {app: stuck}}
    spec:
      containers:
        - {name: main, image: $PAUSE_IMAGE, readinessProbe: {tcpSocket: {port: 8080}, periodSeconds: 2}, resources: {requests: {cpu: 5m, memory: 8Mi}}}
YAML
deployment smoke-stage-b app 0
kubectl apply -f - >/dev/null <<YAML || fail "applying test objects"
apiVersion: finops.costdeck.io/v1
kind: ScalingGroup
metadata: {name: smoke-stages, namespace: $NS}
spec:
  category: Smoke
  namespaces: [smoke-stage-a, smoke-stage-b]
  sequence: [smoke-stage-a, smoke-stage-b]
  featureFlags: {skipOnTimeout: true, timeoutMinutes: 1}
YAML
# No schedule: the group is always on, so it brings both namespaces up, in order.
wait_for "stage 1 started" replicas_are smoke-stage-a stuck 1
holds_for 30 "stage 2 waits for stage 1" replicas_are smoke-stage-b app 0
wait_for "stage 2 started once stage 1 timed out" replicas_are smoke-stage-b app 1
wait_for "the stuck namespace listed as skipped" bash -c \
  "[[ \$(kubectl get scalinggroup smoke-stages -n $NS -o jsonpath='{.status.skippedNamespaces}') == *smoke-stage-a* ]]"
wait_for "a ScalingTimeout event" event_seen smoke-stages ScalingTimeout
wait_for "stage 2 ready" ready_are smoke-stage-b app 1
holds_for 10 "the group stays ScalingUp while a namespace is not up, without flapping" group_field_is smoke-stages phase ScalingUp
