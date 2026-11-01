# shellcheck shell=bash

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it changes the VictoriaMetrics settings of the installed release"
  return 0
fi

java_workload() {
  kubectl apply -f - >/dev/null <<YAML || fail "applying $1"
apiVersion: apps/v1
kind: Deployment
metadata: {name: $1, namespace: smoke-java, annotations: {$2}}
spec:
  replicas: 1
  selector: {matchLabels: {app: $1}}
  template:
    metadata: {labels: {app: $1}}
    spec:
      terminationGracePeriodSeconds: 1
      containers:
        - name: app
          image: $BUSYBOX_IMAGE
          command: [sh, -c, 'sleep 3600']
          env: [$3]
          resources: {requests: {cpu: 100m, memory: $4}, limits: {cpu: "2", memory: $5}}
YAML
}
java_pod() { kubectl get pods -n smoke-java -l "app=$1" -o jsonpath='{.items[0].metadata.name}'; }

log "Java workloads: JVM flags in the pod spec, and JVM heap metrics in VictoriaMetrics"
start_victoriametrics 7
new_namespace smoke-java
java_workload java-default '' '{name: JAVA_TOOL_OPTIONS, value: "-XX:+UseG1GC"}' 2Gi 2Gi
java_workload java-tight '' '{name: JAVA_TOOL_OPTIONS, value: "-Xmx900m -XX:+UseG1GC"}' 256Mi 1Gi
java_workload java-big '' '{name: JAVA_TOOL_OPTIONS, value: "-Xmx3g -XX:+UseG1GC"}' 4Gi 4Gi
java_workload orders '' '' 2Gi 2Gi
java_workload legacy 'costdeck.io/runtime: none' '{name: JAVA_TOOL_OPTIONS, value: "-Xmx3g"}' 512Mi 4Gi
java_workload annotated 'costdeck.io/runtime: java' '' 1Gi 2Gi
for w in java-default java-tight java-big orders legacy annotated; do
  kubectl rollout status "deploy/$w" -n smoke-java --timeout=120s >/dev/null || fail "$w did not start"
done
JAVA_BIG_POD=$(java_pod java-big)
JAVA_ORDERS_POD=$(java_pod orders)

python3 - "$JAVA_BIG_POD" "$JAVA_ORDERS_POD" <<'PY' | curl -sf --data-binary @- "localhost:$VM_PORT/api/v1/import" || fail "importing the synthetic JVM metrics"
import json, sys, time
big, orders = sys.argv[1], sys.argv[2]
now = int(time.time()) // 60 * 60
start, step, mi = now - 2 * 86400, 60, 2**20
def series(labels, fn):
    ts = list(range(start, now + 1, step))
    print(json.dumps({"metric": labels, "values": [fn(t) for t in ts], "timestamps": [t * 1000 for t in ts]}))
eden = lambda peak: (lambda t: (t % 240) / 240 * peak)
pod = {"namespace": "smoke-java", "job": "apps"}
series({"__name__": "jvm_memory_used_bytes", **pod, "pod": big, "container": "app", "area": "heap", "id": "G1 Old Gen"}, lambda t: 400 * mi)
series({"__name__": "jvm_memory_used_bytes", **pod, "pod": big, "container": "app", "area": "heap", "id": "G1 Eden Space"}, eden(2400 * mi))
series({"__name__": "jvm_memory_used_bytes", **pod, "pod": big, "container": "app", "area": "nonheap", "id": "Metaspace"}, lambda t: 150 * mi)
series({"__name__": "jvm_memory_used_bytes", **pod, "pod": orders, "jvm_memory_type": "heap", "jvm_memory_pool_name": "G1 Old Gen"}, lambda t: 200 * mi)
series({"__name__": "jvm_memory_used_bytes", **pod, "pod": orders, "jvm_memory_type": "heap", "jvm_memory_pool_name": "G1 Eden Space"}, eden(600 * mi))
for name, mem in [(big, 3500), (orders, 900)]:
    labels = {"namespace": "smoke-java", "pod": name, "container": "app"}
    series({"__name__": "container_memory_working_set_bytes", **labels}, lambda t, mem=mem: mem * mi)
    series({"__name__": "container_cpu_usage_seconds_total", **labels}, lambda t: round((t - start) * 0.05, 3))
PY
curl -sf "localhost:$VM_PORT/internal/force_flush" >/dev/null
curl -sf "localhost:$VM_PORT/internal/resetRollupResultCache" >/dev/null
ok "imported"
wait_for "VictoriaMetrics serves the JVM heap series" vm_series_at_least 'jvm_memory_used_bytes{namespace="smoke-java"}' 5

log "The advice reads the JVM: flags from the pod spec, the heap from its metrics"
JAVA_ADVICE=$(mktemp)
java_advice_ready() {
  api GET /api/namespaces/smoke-java/recommendations >"$JAVA_ADVICE" &&
    jq -e '.workloads[] | select(.name == "java-big") | .containers[0].jvm.recommendedHeap != null' "$JAVA_ADVICE" >/dev/null
}
TIMEOUT=330 wait_for "Java advice with the heap from VictoriaMetrics, within the five minutes advice is cached" java_advice_ready
java_jvm() { jq -c --arg n "$1" '.workloads[] | select(.name == $n) | .containers[0].jvm' "$JAVA_ADVICE"; }
java_expect() {
  echo "$2" | jq -e "$3" >/dev/null || fail "$1: $2"
  ok "$1"
}
java_expect "the default heap is a quarter of the limit, and that is waste" "$(java_jvm java-default)" \
  '.detectedBy == "options" and .heap == "512Mi" and (.heapSource | test("default, 25%")) and any(.findings[]; .code == "default-heap")'
java_expect "a heap that leaves no room for native memory, above its request" "$(java_jvm java-tight)" \
  '.heap == "900Mi" and any(.findings[]; .code == "no-room-for-native") and any(.findings[]; .code == "request-below-heap")'
jq -e '.workloads[] | select(.name == "java-tight") | .containers[0].memory.action == "increase"' "$JAVA_ADVICE" >/dev/null ||
  fail "the request below the heap is not raised: $(jq -c '.workloads[] | select(.name == "java-tight") | .containers[0].memory' "$JAVA_ADVICE")"
ok "its memory request is raised to cover the heap"
java_expect "a 3Gi heap whose data after GC peaks at 400Mi gets an 800Mi heap" "$(java_jvm java-big)" \
  '.heapAfterGC == "400Mi" and .nonHeap == "150Mi" and .recommendedHeap == "800Mi" and .recommendedMemory == "1044Mi" and .recommendedOptions == "-Xmx800m" and any(.findings[]; .code == "heap-oversized") and .monthlySavings > 0'
java_expect "an image with no Java hints is found by its OpenTelemetry heap metrics" "$(java_jvm orders)" \
  '.detectedBy == "metrics" and .heapAfterGC == "200Mi"'
java_expect "the annotation marks a container as Java" "$(java_jvm annotated)" '.detectedBy == "annotation"'
[[ "$(java_jvm legacy)" == null ]] || fail "costdeck.io/runtime: none still got JVM advice: $(java_jvm legacy)"
ok "costdeck.io/runtime: none opts out"
jq -e '.javaMonthlySavings > 0 and (.basis | test("Java containers"))' "$JAVA_ADVICE" >/dev/null ||
  fail "the report does not total or explain the Java advice: $(jq -c '{javaMonthlySavings, basis}' "$JAVA_ADVICE")"
ok "the report totals the heap savings and says how Java advice is worked out"
rm -f "$JAVA_ADVICE"
api PUT /api/settings '{"integrations":{"victoriaMetrics":{"enabled":false}}}' >/dev/null
