# shellcheck shell=bash
# Helpers shared by the end-to-end scenarios in hack/e2e/*.sh, sourced by hack/e2e-smoke.sh.
# Every scenario creates its own smoke-* namespaces and groups and registers them for
# cleanup, so scenarios do not depend on each other and can run alone (E2E_ONLY).

log() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
ok() { echo "ok: $*"; }
fail() { printf '\033[31mFAIL: %s\033[0m\n' "$*" >&2; dump; exit 1; }

dump() {
  echo "--- scaling groups"; kubectl get scalinggroups -n "$NS" -o wide || true
  echo "--- scaling configs"; kubectl get scalingconfigs -n "$NS" -o wide || true
  echo "--- smoke workloads"; kubectl get deploy -A -l costdeck-e2e=true -o wide || true
  echo "--- recent events"; kubectl get events -n "$NS" --sort-by=.lastTimestamp | tail -30 || true
  echo "--- operator logs"; kubectl logs -n "$NS" "deploy/$RELEASE" --tail=120 || true
}

# wait_for <description> <command...>: retries the command until it succeeds.
wait_for() {
  local what=$1; shift
  local deadline=$((SECONDS + TIMEOUT))
  until "$@" >/dev/null 2>&1; do
    ((SECONDS < deadline)) || fail "timed out waiting for: $what"
    sleep 3
  done
  ok "$what"
}

# holds_for <seconds> <description> <command...>: the command must keep succeeding for the
# whole period. It catches things that must not happen, such as a group starting too early.
holds_for() {
  local seconds=$1 what=$2; shift 2
  local until=$((SECONDS + seconds))
  while ((SECONDS < until)); do
    "$@" >/dev/null 2>&1 || fail "no longer true after $((seconds - until + SECONDS))s: $what"
    sleep 2
  done
  ok "$what (held for ${seconds}s)"
}

replicas_are() { [[ "$(kubectl get deploy "$2" -n "$1" -o jsonpath='{.spec.replicas}')" == "$3" ]]; }
ready_are() { [[ "$(kubectl get deploy "$2" -n "$1" -o jsonpath='{.status.readyReplicas}')" == "$3" ]]; }
group_field_is() { [[ "$(kubectl get scalinggroup "$1" -n "$NS" -o jsonpath="{.status.$2}")" == "$3" ]]; }
group_field_is_not() { ! group_field_is "$@"; }
cronjob_suspended_is() { [[ "$(kubectl get cronjob "$2" -n "$1" -o jsonpath='{.spec.suspend}')" == "$3" ]]; }
exists() { kubectl get "$@" >/dev/null 2>&1; }
missing() { ! exists "$@"; }
event_seen() { kubectl get events -n "$NS" --field-selector "involvedObject.name=$1,reason=$2" -o name | grep -q .; }

# api <method> <path> [json]: calls the API as the signed-in admin and prints the body.
api() {
  curl -sS -b "$JAR" -X "$1" -H 'Content-Type: application/json' ${3:+-d "$3"} "localhost:$API_PORT$2"
}
# api_code <method> <path> [json] [curl args...]: prints only the HTTP status.
api_code() {
  local method=$1 path=$2 body=${3:-}; shift $(($# < 3 ? $# : 3))
  curl -s -o /dev/null -w '%{http_code}' -X "$method" -H 'Content-Type: application/json' ${body:+-d "$body"} "$@" "localhost:$API_PORT$path"
}
expect_code() {
  local want=$1 what=$2 got; shift 2
  got=$(api_code "$@")
  [[ "$got" == "$want" ]] || fail "$what: HTTP $got, want $want"
  ok "$what"
}

# Cleanup registry: scenarios add what they create; cleanup removes it on exit.
CLEANUP_GROUPS=()
CLEANUP_CONFIGS=()
CLEANUP_NAMESPACES=()
CLEANUP_CALLS=()
track_namespace() { CLEANUP_NAMESPACES+=("$@"); }
track_group() { CLEANUP_GROUPS+=("$@"); }
track_config() { CLEANUP_CONFIGS+=("$@"); }
# track_call <method> <path> [json]: an API call that undoes something, run on exit. The
# JSON must contain no spaces.
track_call() { CLEANUP_CALLS+=("$*"); }

new_namespace() {
  track_namespace "$1"
  kubectl create namespace "$1" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
}

# Images that need no Docker Hub pull, so CI is not rate limited.
PAUSE_IMAGE=registry.k8s.io/pause:3.10
BUSYBOX_IMAGE=registry.k8s.io/e2e-test-images/busybox:1.36.1-1

# deployment <ns> <name> <replicas> [ready-after-seconds] [stop-after-seconds]:
# a labelled test workload. ready-after delays readiness; stop-after makes the pod linger
# that long when scaled down (a slow shutdown).
deployment() {
  local ns=$1 name=$2 replicas=$3 ready_after=${4:-0} stop_after=${5:-0}
  local image=$PAUSE_IMAGE command="" probe="" grace=""
  if ((ready_after > 0 || stop_after > 0)); then
    # sleep runs as PID 1 and so ignores SIGTERM: the pod stops after the grace period.
    image=$BUSYBOX_IMAGE
    command="command: [sleep, \"3600\"]"
    grace="terminationGracePeriodSeconds: $((stop_after > 0 ? stop_after : 1))"
  fi
  if ((ready_after > 0)); then
    probe="readinessProbe: {exec: {command: [\"true\"]}, initialDelaySeconds: $ready_after, periodSeconds: 2}"
  fi
  kubectl apply -f - >/dev/null <<EOF || fail "applying deployment $ns/$name"
apiVersion: apps/v1
kind: Deployment
metadata: {name: $name, namespace: $ns, labels: {costdeck-e2e: "true"}}
spec:
  replicas: $replicas
  selector: {matchLabels: {app: $name}}
  template:
    metadata: {labels: {app: $name}}
    spec:
      ${grace}
      containers:
        - name: main
          image: $image
          ${command}
          ${probe}
          resources: {requests: {cpu: 5m, memory: 8Mi}}
EOF
}

# A one-minute window that is practically never open: the schedule is "closed".
# shellcheck disable=SC2034 # used by the scenarios that source this file
CLOSED_SCHEDULE='[{startDay: 0, startTime: "03:00", endDay: 0, endTime: "03:01", timezone: "Pacific/Kiritimati"}]'
# shellcheck disable=SC2034
CLOSED_SCHEDULE_JSON='[{"startDay":0,"startTime":"03:00","endDay":0,"endTime":"03:01","timezone":"Pacific/Kiritimati"}]'
