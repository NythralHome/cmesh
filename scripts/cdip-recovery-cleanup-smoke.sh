#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_CDIP_RECOVERY_CLEANUP_SMOKE_DIR:-/tmp/cmesh-cdip-recovery-cleanup-smoke-$(date -u +%Y%m%d%H%M%S)}"
MANAGER_PORT="${CMESH_CDIP_RECOVERY_CLEANUP_MANAGER_PORT:-0}"
DAEMON_PORT="${CMESH_CDIP_RECOVERY_CLEANUP_DAEMON_PORT:-0}"
SESSION_ID="stage-recovery-cleanup-smoke"
MODEL_ID="${CMESH_MODEL_ID:-qwen2.5-0.5b-instruct-q4-k-m}"
MODEL_PATH="$RUN_DIR/model.gguf"
MANAGER_PID=""
DAEMON_PID=""

mkdir -p "$RUN_DIR"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "FAIL: $1 is required" >&2
    exit 1
  }
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
  for _ in $(seq 1 80); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  echo "FAIL: timed out waiting for $url" >&2
  return 1
}

main() {
  need curl
  need jq
  need python3

  if [[ "$MANAGER_PORT" == "0" ]]; then
    MANAGER_PORT="$(free_port)"
  fi
  if [[ "$DAEMON_PORT" == "0" ]]; then
    DAEMON_PORT="$(free_port)"
  fi

  local bin manager daemon
  bin="$RUN_DIR/cmesh"
  manager="http://127.0.0.1:${MANAGER_PORT}"
  daemon="http://127.0.0.1:${DAEMON_PORT}"

  go build -o "$bin" "$ROOT_DIR/cmd/cmesh"
  touch "$MODEL_PATH"

  "$bin" stage-runner daemon \
    --addr "127.0.0.1:${DAEMON_PORT}" \
    --session-dir "$RUN_DIR/stage-sessions" \
    >"$RUN_DIR/stage-daemon.log" 2>&1 &
  DAEMON_PID=$!

  "$bin" manager start \
    --memory \
    --addr "127.0.0.1:${MANAGER_PORT}" \
    --join-token smoke-token \
    --cdip-auto-advance=false \
    >"$RUN_DIR/manager.log" 2>&1 &
  MANAGER_PID=$!

  trap 'kill "${MANAGER_PID:-}" "${DAEMON_PID:-}" >/dev/null 2>&1 || true' EXIT

  wait_for_http "$manager/health"
  wait_for_http "$daemon/health"

  curl -fsS -X POST "$manager/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"recovery-cleanup-worker\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":4,\"cores_allowed\":2},\"memory\":{\"total_bytes\":8589934592,\"allowed_bytes\":4294967296},\"storage\":{\"total_bytes\":10737418240,\"allowed_bytes\":5368709120,\"free_bytes\":8589934592},\"models\":[{\"id\":\"${MODEL_ID}\",\"runtime\":\"llama.cpp\",\"path\":\"${MODEL_PATH}\",\"ready\":true}],\"runtimes\":[{\"name\":\"llama.cpp\",\"ready\":true,\"capabilities\":[\"logical-stage-runtime\",\"llama.cpp-stage-runner\"],\"stage_runtimes\":[{\"name\":\"cmesh-stage-daemon\",\"ready\":true,\"endpoint\":\"${daemon}\",\"protocol\":\"cdip.stage-session-v1\"}]}]},\"join_token\":\"smoke-token\"}" \
    >"$RUN_DIR/join-worker.json"
  local node_id
  node_id="$(jq -r '.node_id' "$RUN_DIR/join-worker.json")"

  curl -fsS -X POST "$manager/v1/jobs" \
    -H 'Content-Type: application/json' \
    -d '{"type":"model.generate.distributed","input":"{\"model_id\":\"qwen2.5-0.5b-instruct-q4-k-m\",\"prompt\":\"recovery cleanup smoke\"}","requested_by":"recovery-cleanup-smoke","no_auto_assign":true}' \
    >"$RUN_DIR/parent-job.json"
  local parent_id
  parent_id="$(jq -r '.id' "$RUN_DIR/parent-job.json")"

  jq -n \
    --arg session_id "$SESSION_ID" \
    --arg parent_id "$parent_id" \
    --arg model_id "$MODEL_ID" \
    --arg model_path "$MODEL_PATH" \
    '{
      session_id: $session_id,
      parent_job_id: $parent_id,
      stage_job_id: "job-stage-recovery-cleanup",
      model_id: $model_id,
      model_path: $model_path,
      stage_index: 0,
      layer_start: 0,
      layer_end: 1,
      kv_cache_key: "recovery-cleanup-smoke"
    }' \
    | curl -fsS -X POST "$daemon/v1/sessions" -H 'Content-Type: application/json' -d @- \
    >"$RUN_DIR/session-created.json"

  jq -e '.session_id == "stage-recovery-cleanup-smoke"' "$RUN_DIR/session-created.json" >/dev/null
  curl -fsS "$daemon/v1/sessions/$SESSION_ID" >"$RUN_DIR/session-before-cancel.json"

  local stage_input
  stage_input="$(jq -nc \
    --arg parent_id "$parent_id" \
    --arg node_id "$node_id" \
    --arg model_id "$MODEL_ID" \
    --arg daemon "$daemon" \
    --arg session_id "$SESSION_ID" \
    '{
      parent_job_id: $parent_id,
      stage_job_id: "job-stage-recovery-cleanup",
      model_id: $model_id,
      stage: {index: 0, node_id: $node_id, layer_start: 0, layer_end: 1, layers: 2},
      shard: {stage: {index: 0, node_id: $node_id, layer_start: 0, layer_end: 1}, runtime: "llama.cpp", materialization: "logical-layers"},
      stage_daemon_url: $daemon,
      stage_session_id: $session_id,
      kv_cache_key: "recovery-cleanup-smoke"
    }')"

  jq -n \
    --arg input "$stage_input" \
    --arg node_id "$node_id" \
    --arg parent_id "$parent_id" \
    '{
      type: "model.generate.distributed.stage",
      input: $input,
      requested_by: ("distributed-coordinator:" + $parent_id),
      assigned_to: $node_id,
      cdip_state: "decode",
      cdip_parent_job_id: $parent_id,
      cdip_stage_index: 0
    }' \
    | curl -fsS -X POST "$manager/v1/jobs" -H 'Content-Type: application/json' -d @- \
    >"$RUN_DIR/stage-job.json"

  curl -fsS -X POST "$manager/v1/jobs/$parent_id/cancel" >"$RUN_DIR/parent-cancel.json"
  jq -e '.status == "canceled"' "$RUN_DIR/parent-cancel.json" >/dev/null

  if curl -fsS "$daemon/v1/sessions/$SESSION_ID" >"$RUN_DIR/session-after-cancel.json" 2>/dev/null; then
    echo "FAIL: resident stage daemon session still exists after parent cancel" >&2
    exit 1
  fi

  curl -fsS "$daemon/health" >"$RUN_DIR/stage-daemon-health-after-cancel.json"
  jq -e '.session_count == 0' "$RUN_DIR/stage-daemon-health-after-cancel.json" >/dev/null

  curl -fsS "$manager/v1/jobs" >"$RUN_DIR/jobs-after-cancel.json"
  jq -e --arg parent_id "$parent_id" '
    (.jobs | any(.id == $parent_id and .status == "canceled")) and
    (.jobs | any(.cdip_parent_job_id == $parent_id and .status == "canceled" and .cdip_state == "aborted"))
  ' "$RUN_DIR/jobs-after-cancel.json" >/dev/null

  jq -n \
    --arg manager "$manager" \
    --arg daemon "$daemon" \
    --arg parent_id "$parent_id" \
    --arg session_id "$SESSION_ID" \
    --arg evidence "$RUN_DIR" \
    '{
      status: "passed",
      manager: $manager,
      daemon: $daemon,
      parent_job_id: $parent_id,
      cleaned_session_id: $session_id,
      evidence: $evidence
    }' >"$RUN_DIR/summary.json"

  echo "PASS: CDIP recovery cleanup smoke succeeded"
  echo "Evidence: $RUN_DIR"
}

main "$@"
