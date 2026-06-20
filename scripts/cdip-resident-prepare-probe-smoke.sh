#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_CDIP_RESIDENT_PREPARE_PROBE_SMOKE_DIR:-/tmp/cmesh-cdip-resident-prepare-probe-smoke-$(date -u +%Y%m%d%H%M%S)}"
PORT="${CMESH_CDIP_RESIDENT_PREPARE_PROBE_SMOKE_PORT:-0}"
MODEL_ID="${CMESH_MODEL_ID:-qwen2.5-0.5b-instruct-q4-k-m}"
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

  mkdir -p "$RUN_DIR"
  if [[ "$PORT" == "0" ]]; then
    PORT="$(free_port)"
  fi

  local bin runner model session_dir daemon session_id
  bin="$RUN_DIR/cmesh"
  runner="$RUN_DIR/cmesh-stage-runner"
  model="$RUN_DIR/model.gguf"
  session_dir="$RUN_DIR/stage-sessions"
  daemon="http://127.0.0.1:$PORT"
  session_id="stage-0-resident-smoke"

  go build -o "$bin" "$ROOT_DIR/cmd/cmesh"
  cat >"$runner" <<'SH'
#!/usr/bin/env sh
set -eu
stage_index=0
stage_start=0
stage_end=0
model=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --stage-index) shift; stage_index="$1" ;;
    --stage-start) shift; stage_start="$1" ;;
    --stage-end) shift; stage_end="$1" ;;
    --model) shift; model="$1" ;;
  esac
  shift
done
printf '{"kind":"cmesh.llamacpp_stage_prepare","status":"metadata_ready","runtime":"llama.cpp","model_path":"%s","stage_index":%s,"stage_start":%s,"stage_end":%s}\n' "$model" "$stage_index" "$stage_start" "$stage_end"
SH
  chmod +x "$runner"
  printf 'fake resident model\n' >"$model"

  "$bin" stage-runner daemon \
    --addr "127.0.0.1:$PORT" \
    --session-dir "$session_dir" \
    --backend llama.cpp-resident \
    --runner-bin "$runner" \
    >"$RUN_DIR/stage-daemon.log" 2>&1 &
  DAEMON_PID=$!
  trap cleanup EXIT

  for _ in $(seq 1 80); do
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
    .backend_status.ready == false and
    .backend_status.runner_ready == true and
    .backend_status.runner_bin == $runner and
    ((.backend_status.missing_hooks // []) | length == 3)
  ' "$RUN_DIR/health.json" >/dev/null

  jq -n \
    --arg session_id "$session_id" \
    --arg model_id "$MODEL_ID" \
    --arg model_path "$model" \
    '{
      session_id:$session_id,
      parent_job_id:"job-resident-smoke",
      stage_job_id:"job-stage-0",
      model_id:$model_id,
      model_path:$model_path,
      stage_index:0,
      layer_start:0,
      layer_end:7,
      kv_cache_key:"resident-smoke:kv"
    }' \
    | curl -fsS -X POST "$daemon/v1/sessions" -H 'Content-Type: application/json' -d @- \
    >"$RUN_DIR/session.json"

  jq -e --arg model "$model" '
    .protocol == "cdip.stage-session-v1" and
    .mode == "daemon" and
    .session_id == "stage-0-resident-smoke" and
    .runtime_backend == "llama.cpp-resident" and
    .runtime_status == "prepare_probe_ready_missing_native_decode_hooks" and
    .model_path == $model and
    .persistent_model == false and
    .persistent_kv_in_memory == false and
    .ready == false
  ' "$RUN_DIR/session.json" >/dev/null

  curl -fsS "$daemon/v1/sessions/$session_id" >"$RUN_DIR/session-record.json"
  jq -e --arg model "$model" '
    .backend_kind == "llama.cpp-resident" and
    .native_kv == true and
    .decode_steps == 0 and
    .session.model_path == $model and
    .session.runtime_status == "prepare_probe_ready_missing_native_decode_hooks"
  ' "$RUN_DIR/session-record.json" >/dev/null

  [[ -f "$session_dir/$session_id.json" ]] || {
    echo "FAIL: expected persisted resident session metadata" >&2
    exit 1
  }

  cat >"$RUN_DIR/summary.json" <<JSON
{
  "status": "passed",
  "daemon": "$daemon",
  "backend": "llama.cpp-resident",
  "runner": "$runner",
  "model": "$model",
  "session_id": "$session_id"
}
JSON

  echo "PASS: CDIP resident prepare probe smoke succeeded"
  echo "Evidence: $RUN_DIR"
}

main "$@"
