#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_OBSERVABILITY_SMOKE_DIR:-${TMPDIR:-/tmp}/cmesh-observability-smoke-$(date -u +%Y%m%d%H%M%S)}"
PORT="${CMESH_OBSERVABILITY_SMOKE_PORT:-0}"
JOIN_TOKEN="observability-smoke-join-token"
OPERATOR_TOKEN="observability-smoke-operator-token"
MANAGER_PID=""

cleanup() {
  if [[ -n "${MANAGER_PID:-}" ]]; then
    kill "$MANAGER_PID" >/dev/null 2>&1 || true
    wait "$MANAGER_PID" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

free_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

wait_for_http() {
  local url="$1"
  local i
  for i in $(seq 1 80); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

http_status() {
  local url="$1"
  curl -sS -o /dev/null -w '%{http_code}' "$url"
}

main() {
  need curl
  need jq
  need python3

  mkdir -p "$RUN_DIR"
  if [[ "$PORT" == "0" ]]; then
    PORT="$(free_port)"
  fi
  local bin manager
  bin="$RUN_DIR/cmesh"
  manager="http://127.0.0.1:${PORT}"

  (cd "$ROOT_DIR" && go build -o "$bin" ./cmd/cmesh)

  "$bin" manager start \
    --memory \
    --addr "127.0.0.1:${PORT}" \
    --join-token "$JOIN_TOKEN" \
    --operator-token "$OPERATOR_TOKEN" \
    --cdip-auto-advance=false \
    >"$RUN_DIR/manager.log" 2>&1 &
  MANAGER_PID=$!
  wait_for_http "$manager/health" || fail "manager did not become healthy"

  local unauthorized_status
  unauthorized_status="$(http_status "$manager/v1/observability")"
  [[ "$unauthorized_status" == "401" ]] || fail "observability endpoint did not require operator token"

  curl -fsS -H "X-CMesh-Operator-Token: $OPERATOR_TOKEN" \
    "$manager/v1/observability" >"$RUN_DIR/observability-empty.json"
  jq -e '
    .status == "degraded" and
    .cluster.workers_online == 0 and
    (.blockers | index("no online workers"))
  ' "$RUN_DIR/observability-empty.json" >/dev/null ||
    fail "empty observability report did not expose no-worker degraded status"

  curl -fsS -X POST "$manager/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"observability-worker\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":4,\"cores_allowed\":2},\"memory\":{\"total_bytes\":8589934592,\"allowed_bytes\":4294967296},\"storage\":{\"total_bytes\":42949672960,\"allowed_bytes\":21474836480,\"free_bytes\":32212254720},\"runtimes\":[{\"name\":\"llama.cpp\",\"ready\":true,\"capabilities\":[\"llama.cpp-stage-runner\"],\"stage_runtimes\":[{\"name\":\"cmesh-stage-daemon\",\"ready\":true,\"endpoint\":\"http://127.0.0.1:19781\",\"protocol\":\"cdip.stage-session-v1\"}]}]},\"join_token\":\"$JOIN_TOKEN\"}" \
    >"$RUN_DIR/join-worker.json"
  local node_id
  node_id="$(jq -r '.node_id' "$RUN_DIR/join-worker.json")"
  [[ -n "$node_id" && "$node_id" != "null" ]] || fail "worker join did not return node_id"

  curl -fsS -X POST "$manager/v1/jobs" \
    -H "X-CMesh-Operator-Token: $OPERATOR_TOKEN" \
    -H 'Content-Type: application/json' \
    -d '{"type":"model.generate.distributed","input":"{\"model_id\":\"qwen2.5-0.5b-instruct-q4-k-m\",\"prompt\":\"observability smoke\"}","requested_by":"observability-smoke","no_auto_assign":true}' \
    >"$RUN_DIR/parent-job.json"

  curl -fsS -H "X-CMesh-Operator-Token: $OPERATOR_TOKEN" \
    "$manager/v1/observability" >"$RUN_DIR/observability-ready.json"

  jq -e --arg node_id "$node_id" '
    .status == "ok" and
    .cluster.workers_total == 1 and
    .cluster.workers_online == 1 and
    .cluster.resources.cpu.cores_allowed == 2 and
    (.workers | length) == 1 and
    .workers[0].id == $node_id and
    .workers[0].stage_daemons[0].protocol == "cdip.stage-session-v1" and
    .workers[0].stage_daemons[0].endpoint == "http://127.0.0.1:19781" and
    .stage_daemons.workers_total == 1 and
    .stage_daemons.ready == 1 and
    .jobs.total == 1 and
    .jobs.active == 1 and
    .jobs.by_status.queued == 1 and
    .jobs.by_type["model.generate.distributed"] == 1 and
    (.recent_distributed_jobs | length) == 1 and
    .recent_distributed_jobs[0].type == "model.generate.distributed" and
    .cdip.parent_jobs_total == 1 and
    .recovery.enabled == true and
    .recovery.stale_after_ms == 120000 and
    ((.blockers // []) | length == 0)
  ' "$RUN_DIR/observability-ready.json" >/dev/null ||
    fail "ready observability report did not expose expected production counters"

  cat >"$RUN_DIR/summary.txt" <<EOF
PASS: observability smoke succeeded
manager: $manager
node_id: $node_id
evidence: $RUN_DIR
EOF
  cat "$RUN_DIR/summary.txt"
}

main "$@"
