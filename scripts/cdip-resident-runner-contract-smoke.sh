#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_CDIP_RESIDENT_RUNNER_CONTRACT_SMOKE_DIR:-/tmp/cmesh-cdip-resident-runner-contract-smoke-$(date -u +%Y%m%d%H%M%S)}"
PORT="${CMESH_CDIP_RESIDENT_RUNNER_CONTRACT_SMOKE_PORT:-0}"
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
  session_id="stage-0-resident-ready-smoke"

  go build -o "$bin" "$ROOT_DIR/cmd/cmesh"
  cat >"$runner" <<'SH'
#!/usr/bin/env sh
set -eu
command=""
stage_index=0
stage_start=0
stage_end=0
model=""
session_id=""
step=1
while [ "$#" -gt 0 ]; do
  case "$1" in
    --command) shift; command="$1" ;;
    --stage-index) shift; stage_index="$1" ;;
    --stage-start) shift; stage_start="$1" ;;
    --stage-end) shift; stage_end="$1" ;;
    --model) shift; model="$1" ;;
    --session-id) shift; session_id="$1" ;;
    --step) shift; step="$1" ;;
  esac
  shift
done
case "$command" in
  resident-capabilities)
    printf '{"kind":"cmesh.llamacpp_resident_capabilities","protocol":"cdip.llamacpp-resident-runner-v1","ready":true,"native_kv":true,"persistent_model":true,"persistent_kv_in_memory":true,"prepare_hook":true,"decode_hook":true,"source_decode_hook":true,"relay_decode_hook":true,"terminal_decode_hook":true}\n'
    ;;
  prepare)
    printf '{"kind":"cmesh.llamacpp_stage_prepare","status":"metadata_ready","runtime":"llama.cpp","model_path":"%s","stage_index":%s,"stage_start":%s,"stage_end":%s}\n' "$model" "$stage_index" "$stage_start" "$stage_end"
    ;;
  resident-loop)
    sessions=0
    line_value() {
      key="$1"
      line="$2"
      printf '%s\n' "$line" | tr ' ' '\n' | sed -n "s/^${key}=//p" | head -n 1
    }
    while IFS= read -r line; do
      case "$line" in
        command=capabilities)
          printf '{"kind":"cmesh.llamacpp_resident_loop_capabilities","protocol":"cdip.llamacpp-resident-loop-v1","runner_protocol":"cdip.llamacpp-resident-runner-v1","ready":true,"persistent_process":true,"native_kv":true,"persistent_model":true,"persistent_kv_in_memory":true,"prepare_hook":true,"decode_hook":true,"source_decode_hook":true,"relay_decode_hook":true,"terminal_decode_hook":true,"session_count":%s}\n' "$sessions"
          ;;
        command=prepare*)
          sessions=1
          loop_session_id="$(line_value session_id "$line")"
          printf '{"kind":"cmesh.llamacpp_resident_loop_prepare","protocol":"cdip.llamacpp-resident-loop-v1","status":"resident_ready","session_registered":true,"session_id":"%s","persistent_model":true,"persistent_kv_in_memory":true,"n_layer":8,"n_embd":4,"selected_tensor_count":1,"selected_bytes":16}\n' "$loop_session_id"
          ;;
        command=decode*)
          loop_session_id="$(line_value session_id "$line")"
          line_step="$(line_value step "$line")"
          if [ -z "$line_step" ]; then
            line_step=1
          fi
          printf '{"kind":"cmesh.llamacpp_resident_loop_decode","protocol":"cdip.llamacpp-resident-loop-v1","status":"resident_decoded","session_id":"%s","session_found":true,"decode_steps":1,"persistent_model":true,"persistent_kv_in_memory":true,"sequence":%s,"checksum":"sha256:resident-%s","payload_bytes":16,"output_bytes":0,"decode_status":0}\n' "$loop_session_id" "$line_step" "$line_step"
          ;;
        command=shutdown)
          printf '{"kind":"cmesh.llamacpp_resident_loop_shutdown","protocol":"cdip.llamacpp-resident-loop-v1","status":"closing","session_count":%s}\n' "$sessions"
          exit 0
          ;;
      esac
    done
    ;;
  resident-decode)
    printf '{"kind":"cmesh.llamacpp_resident_decode","status":"decoded","session_id":"%s","sequence":%s,"checksum":"sha256:resident-%s"}\n' "$session_id" "$step" "$step"
    ;;
  *)
    echo "unsupported command $command" >&2
    exit 2
    ;;
esac
SH
  chmod +x "$runner"
  printf 'fake resident-ready model\n' >"$model"

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
    .backend_status.runner_ready == true and
    .backend_status.runner_bin == $runner and
    .backend_status.resident_protocol == "cdip.llamacpp-resident-runner-v1" and
    .backend_status.decode_ready == true and
    .backend_status.ready == true and
    ((.backend_status.missing_hooks // []) | length == 0)
  ' "$RUN_DIR/health.json" >/dev/null

  jq -n \
    --arg session_id "$session_id" \
    --arg model_id "$MODEL_ID" \
    --arg model_path "$model" \
    '{
      session_id:$session_id,
      parent_job_id:"job-resident-ready-smoke",
      stage_job_id:"job-stage-0",
      model_id:$model_id,
      model_path:$model_path,
      stage_index:0,
      layer_start:0,
      layer_end:7,
      kv_cache_key:"resident-ready-smoke:kv"
    }' \
    | curl -fsS -X POST "$daemon/v1/sessions" -H 'Content-Type: application/json' -d @- \
    >"$RUN_DIR/session.json"

  jq -e --arg model "$model" '
    .protocol == "cdip.stage-session-v1" and
    .mode == "daemon" and
    .session_id == "stage-0-resident-ready-smoke" and
    .runtime_backend == "llama.cpp-resident" and
    .runtime_status == "resident_loop_ready" and
    .model_path == $model and
    .persistent_model == true and
    .persistent_kv_in_memory == true and
    .ready == true
  ' "$RUN_DIR/session.json" >/dev/null

  python3 - "$RUN_DIR/decode-request.json" <<'PY'
import base64
import hashlib
import json
import sys

payload = b"0123456789abcdef"
request = {
    "step": 2,
    "stage_command": "relay_decode",
    "activation_payload_base64": base64.b64encode(payload).decode(),
    "tensor_envelope": {
        "protocol": "cdip.tensor-envelope-v1",
        "dtype": "f32",
        "shape": [1, 4],
        "byte_count": len(payload),
        "checksum": "sha256:" + hashlib.sha256(payload).hexdigest(),
        "sequence": 2,
        "stage_index": 0,
        "kv_cache_key": "resident-ready-smoke:kv",
    },
}
with open(sys.argv[1], "w", encoding="utf-8") as f:
    json.dump(request, f)
PY

  curl -fsS -X POST "$daemon/v1/sessions/$session_id/decode" -H 'Content-Type: application/json' -d @"$RUN_DIR/decode-request.json" \
    >"$RUN_DIR/decode.json"

  jq -e '{
    step:2,
    stage_command:"relay_decode",
    payload_bytes:(.activation_payload_base64 | @base64d | length),
    checksum:.tensor_envelope.checksum
  } | .payload_bytes == 16 and (.checksum | startswith("sha256:"))' "$RUN_DIR/decode-request.json" >/dev/null

  jq -e '
    .kind == "cmesh.stage_daemon_decode" and
    .session_id == "stage-0-resident-ready-smoke" and
    .step == 2 and
    .decode_steps == 1 and
    .last_stage_command == "relay_decode" and
    .last_payload_bytes == 16 and
    .backend == "llama.cpp-resident" and
    .native_kv == true and
    .persistent_model == true and
    .persistent_kv_in_memory == true and
    .ready == true and
    .last_sequence == 2 and
    .last_checksum == "sha256:resident-2"
  ' "$RUN_DIR/decode.json" >/dev/null

  curl -fsS "$daemon/v1/sessions/$session_id" >"$RUN_DIR/session-record.json"
  jq -e '
    .backend_kind == "llama.cpp-resident" and
    .native_kv == true and
    .decode_steps == 1 and
    .last_step == 2 and
    .last_stage_command == "relay_decode" and
    .last_payload_bytes == 16 and
    .last_sequence == 2 and
    .last_checksum == "sha256:resident-2" and
    .session.runtime_status == "resident_loop_ready" and
    .session.persistent_kv_in_memory == true
  ' "$RUN_DIR/session-record.json" >/dev/null

  [[ -f "$session_dir/$session_id.json" ]] || {
    echo "FAIL: expected persisted resident-ready session metadata" >&2
    exit 1
  }

  cat >"$RUN_DIR/summary.json" <<JSON
{
  "status": "passed",
  "daemon": "$daemon",
  "backend": "llama.cpp-resident",
  "resident_protocol": "cdip.llamacpp-resident-runner-v1",
  "runner": "$runner",
  "model": "$model",
  "session_id": "$session_id",
  "decode_steps": 1
}
JSON

  echo "PASS: CDIP resident runner contract smoke succeeded"
  echo "Evidence: $RUN_DIR"
}

main "$@"
