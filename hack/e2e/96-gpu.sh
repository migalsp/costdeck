# shellcheck shell=bash
# GPUs are priced apart and charged to the namespaces that request them, and DCGM usage
# from VictoriaMetrics shows which sit idle. The test cluster has no GPU: the scenario
# advertises one the way Kubernetes documents for extended resources, by patching the
# node's status, and feeds VictoriaMetrics synthetic DCGM samples.

if [[ -z "$DISPOSABLE" ]]; then
  echo "skip: it advertises GPUs on a node and changes the VictoriaMetrics settings"
  return 0
fi

GPU_NODE=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')
GPU_ADD='[{"op":"add","path":"/status/capacity/nvidia.com~1gpu","value":"4"}]'
GPU_REMOVE='[{"op":"remove","path":"/status/capacity/nvidia.com~1gpu"}]'
overview() { api GET /api/finops/overview; }
gpu_row() { overview | jq -c '.namespaces[] | select(.name == "smoke-gpu") | .gpu'; }
gpu_requested_is() { [[ "$(gpu_row | jq -r '.requested // empty')" == "$1" ]]; }
allocatable_gpus_are() { [[ "$(kubectl get node "$GPU_NODE" -o jsonpath='{.status.allocatable.nvidia\.com/gpu}')" == "$1" ]]; }
idle_gpu_advice() { overview | jq -e '.opportunities[] | select(.kind == "idle-gpu" and .namespace == "smoke-gpu")' >/dev/null; }

log "A namespace pays for the GPUs its pods request"
track_cleanup "kubectl patch node $GPU_NODE --subresource=status --type=json -p '$GPU_REMOVE'; kubectl label node $GPU_NODE nvidia.com/gpu.product-"
kubectl patch node "$GPU_NODE" --subresource=status --type=json -p "$GPU_ADD" >/dev/null || fail "advertising GPUs on $GPU_NODE"
kubectl label node "$GPU_NODE" nvidia.com/gpu.product=NVIDIA-A10G --overwrite >/dev/null
wait_for "the kubelet makes the GPUs allocatable" allocatable_gpus_are 4
new_namespace smoke-gpu
kubectl apply -f - >/dev/null <<YAML || fail "applying the GPU workload"
apiVersion: apps/v1
kind: Deployment
metadata: {name: train, namespace: smoke-gpu}
spec:
  replicas: 1
  selector: {matchLabels: {app: train}}
  template:
    metadata: {labels: {app: train}}
    spec:
      containers:
        - name: main
          image: $PAUSE_IMAGE
          resources: {requests: {cpu: 5m, memory: 8Mi}, limits: {nvidia.com/gpu: 2}}
YAML
kubectl rollout status deploy/train -n smoke-gpu --timeout=120s >/dev/null || fail "the GPU pod did not start"
wait_for "the overview charges smoke-gpu for two GPUs" gpu_requested_is 2
row=$(gpu_row)
echo "$row" | jq -e '.monthlyCost > 0 and .models == ["NVIDIA-A10G"]' >/dev/null || fail "the GPU row is wrong: $row"
data=$(overview)
echo "$data" | jq -e '.cluster.gpus >= 4 and .monthly.gpu > 0' >/dev/null ||
  fail "the cluster's GPUs are missing: $(echo "$data" | jq -c '{gpus: .cluster.gpus, gpu: .monthly.gpu}')"
nodes=$(api GET /api/finops/nodes)
echo "$nodes" | jq -e --arg n "$GPU_NODE" '.report.nodes[] | select(.name == $n) | .gpus == 4 and .gpuRequested == 2 and .gpuModel == "NVIDIA-A10G"' >/dev/null ||
  fail "the node view does not show the GPUs: $(echo "$nodes" | jq -c --arg n "$GPU_NODE" '.report.nodes[] | select(.name == $n) | {gpus, gpuRequested, gpuModel}')"
ok "two A10Gs charged to smoke-gpu, four on the node"

log "DCGM usage shows the GPUs sit idle"
start_victoriametrics 7
python3 - <<'PY' | curl -sf --data-binary @- "localhost:$VM_PORT/api/v1/import" || fail "importing the DCGM samples"
import json, time
now = int(time.time()) // 60 * 60
ts = list(range(now - 7200, now + 1, 120))
for gpu in ["0", "1"]:
    labels = {"namespace": "smoke-gpu", "pod": "train-0", "container": "main", "gpu": gpu, "modelName": "NVIDIA A10G"}
    for name, value in [("DCGM_FI_DEV_GPU_UTIL", 3), ("DCGM_FI_DEV_FB_USED", 2048)]:
        print(json.dumps({"metric": {"__name__": name, **labels}, "values": [value] * len(ts), "timestamps": [t * 1000 for t in ts]}))
PY
curl -sf "localhost:$VM_PORT/internal/force_flush" >/dev/null
# GPU usage is cached for two minutes.
TIMEOUT=150 wait_for "the overview names the idle GPUs" idle_gpu_advice
row=$(gpu_row)
echo "$row" | jq -e '.utilization > 0.02 and .utilization < 0.04 and .memoryUsedGiB == 2' >/dev/null || fail "DCGM usage is wrong: $row"
ok "busy 3%, at most 2 GiB"
