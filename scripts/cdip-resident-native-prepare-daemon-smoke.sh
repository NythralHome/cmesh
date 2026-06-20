#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_CDIP_RESIDENT_NATIVE_PREPARE_DAEMON_SMOKE_DIR:-/tmp/cmesh-cdip-resident-native-prepare-daemon-smoke-$(date -u +%Y%m%d%H%M%S)}"
WORK_DIR="$RUN_DIR/llama-worktree"
PORT="${CMESH_CDIP_RESIDENT_NATIVE_PREPARE_DAEMON_SMOKE_PORT:-0}"
DAEMON_PID=""

cleanup() {
  if [[ -n "${DAEMON_PID:-}" ]]; then
    kill "$DAEMON_PID" >/dev/null 2>&1 || true
  fi
}

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

main() {
  need curl
  need jq
  need go
  need python3
  need cmake
  need git

  mkdir -p "$RUN_DIR"
  if [[ "$PORT" == "0" ]]; then
    PORT="$(free_port)"
  fi

  local model_path
  if ! model_path="$(cd "$ROOT_DIR" && scripts/ensure-gguf-fixture.sh)"; then
    echo "SKIP: set CMESH_GGUF_MODEL_PATH or CMESH_DOWNLOAD_GGUF_FIXTURE=1 to run resident native-prepare daemon smoke" >&2
    exit 0
  fi

  local bin runner session_dir daemon session_id
  bin="$RUN_DIR/cmesh"
  session_dir="$RUN_DIR/stage-sessions"
  daemon="http://127.0.0.1:$PORT"
  session_id="stage-0-native-prepare-daemon-smoke"

  go build -o "$bin" "$ROOT_DIR/cmd/cmesh"
  (
    cd "$ROOT_DIR"
    WORK_DIR="$WORK_DIR" JOBS="${JOBS:-2}" scripts/prepare-llamacpp-stage-runner-worktree.sh > "$RUN_DIR/build-stage-runner.log"
  )
  runner="$WORK_DIR/src/build-cmesh-stage/bin/cmesh-stage-runner"
  [[ -x "$runner" ]] || {
    echo "FAIL: missing stage runner binary: $runner" >&2
    exit 1
  }

  CMESH_RESIDENT_STAGE_CTX="${CMESH_RESIDENT_STAGE_CTX:-64}" \
  "$bin" stage-runner daemon \
    --addr "127.0.0.1:$PORT" \
    --session-dir "$session_dir" \
    --backend llama.cpp-resident \
    --runner-bin "$runner" \
    >"$RUN_DIR/stage-daemon.log" 2>&1 &
  DAEMON_PID=$!
  trap cleanup EXIT

  for _ in $(seq 1 120); do
    if curl -fsS "$daemon/health" >"$RUN_DIR/health.json" 2>/dev/null; then
      break
    fi
    sleep 0.1
  done

  jq -e --arg runner "$runner" '
    .status == "ok" and
    .protocol == "cdip.stage-session-v1" and
    .backend == "llama.cpp-resident" and
    .backend_status.kind == "llama.cpp-resident" and
    .backend_status.native_kv == true and
    .backend_status.runner_ready == true and
    .backend_status.runner_bin == $runner and
    .backend_status.resident_protocol == "cdip.llamacpp-resident-runner-v1" and
    .backend_status.decode_ready == true and
    .backend_status.ready == true
  ' "$RUN_DIR/health.json" >/dev/null

  jq -n \
    --arg session_id "$session_id" \
    --arg model_path "$model_path" \
    '{
      session_id:$session_id,
      parent_job_id:"job-native-prepare-daemon-smoke",
      stage_job_id:"job-stage-0",
      model_id:"qwen2.5-0.5b-instruct-q4-k-m",
      model_path:$model_path,
      stage_index:0,
      layer_start:0,
      layer_end:0,
      kv_cache_key:"native-prepare-daemon-smoke:kv"
    }' \
    | curl -fsS -X POST "$daemon/v1/sessions" -H 'Content-Type: application/json' -d @- \
    >"$RUN_DIR/session.json"

  jq -e --arg model "$model_path" '
    .protocol == "cdip.stage-session-v1" and
    .mode == "daemon" and
    .session_id == "stage-0-native-prepare-daemon-smoke" and
    .runtime_backend == "llama.cpp-resident" and
    .runtime_status == "resident_loop_ready" and
    .model_path == $model and
    .persistent_model == true and
    .persistent_kv_in_memory == true and
    .ready == true
  ' "$RUN_DIR/session.json" >/dev/null

  curl -fsS "$daemon/v1/sessions/$session_id" >"$RUN_DIR/session-record.json"
  jq -e '
    .backend_kind == "llama.cpp-resident" and
    .native_kv == true and
    .session.runtime_status == "resident_loop_ready" and
    .session.persistent_model == true and
    .session.persistent_kv_in_memory == true and
    .session.ready == true
  ' "$RUN_DIR/session-record.json" >/dev/null

  [[ -f "$session_dir/$session_id.json" ]] || {
    echo "FAIL: expected persisted native-prepare session metadata" >&2
    exit 1
  }

  echo "PASS: CDIP resident native-prepare daemon smoke succeeded"
  echo "Evidence: $RUN_DIR"
}

main "$@"
