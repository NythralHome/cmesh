#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_CDIP_DAEMON_DECODE_LOOP_SMOKE_DIR:-/tmp/cmesh-cdip-daemon-decode-loop-smoke-$(date -u +%Y%m%d%H%M%S)}"
MANAGER_PORT="${CMESH_CDIP_DAEMON_DECODE_LOOP_SMOKE_MANAGER_PORT:-0}"
DAEMON_PORT="${CMESH_CDIP_DAEMON_DECODE_LOOP_SMOKE_DAEMON_PORT:-0}"
MODEL_ID="${CMESH_MODEL_ID:-gemma-3-12b-it-q4-k-m}"
MODEL_PATH="$RUN_DIR/model.gguf"
WORK_DIR="$RUN_DIR/stage-work"
TIMEOUT_MS="${CMESH_STAGE_TIMEOUT_MS:-5000}"

mkdir -p "$RUN_DIR"

BIN="$RUN_DIR/cmesh"
go build -o "$BIN" "$ROOT_DIR/cmd/cmesh"

FAKE_RUNNER="$RUN_DIR/cmesh-stage-runner"
cat >"$FAKE_RUNNER" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

arg_value() {
  local key="$1"
  shift
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "$key" ]; then
      shift
      printf '%s' "${1:-}"
      return 0
    fi
    shift
  done
}

command="$(arg_value --command "$@")"
stage_index="$(arg_value --stage-index "$@")"
stage_start="$(arg_value --stage-start "$@")"
stage_end="$(arg_value --stage-end "$@")"
case "$command" in
  prepare)
    cat <<JSON
{
  "kind": "cmesh.llamacpp_stage_prepare",
  "status": "metadata_ready",
  "runtime": "llama.cpp",
  "model_path": "$(arg_value --model "$@")",
  "model_name": "fake-daemon-loop-model",
  "stage_index": ${stage_index:-0},
  "stage_start": ${stage_start:-0},
  "stage_end": ${stage_end:-0},
  "tensor_manifest": {
    "source": "fake metadata",
    "manifest_only": true,
    "total_tensor_count": 4,
    "selected_tensor_count": 2,
    "stage_tensor_count": 1,
    "boundary_tensor_count": 1,
    "selected_bytes": 16,
    "tensors": [
      {"name": "token_embd.weight", "type": "F32", "bytes": 8, "boundary": true},
      {"name": "blk.0.fake.weight", "type": "F32", "bytes": 8, "boundary": false}
    ]
  },
  "executable": false,
  "guardrail": "fake stage runner for daemon decode-loop smoke"
}
JSON
    ;;
  write-shard-bundle)
    output_file="$(arg_value --output-file "$@")"
    mkdir -p "$(dirname "$output_file")"
    python3 - "$output_file" <<'PY'
import sys

with open(sys.argv[1], "wb") as f:
    f.write(b"CMESH_FAKE_BUNDLE")
PY
    cat <<JSON
{
  "kind": "cmesh.llamacpp_stage_shard_bundle",
  "status": "bundle_ready_not_loadable_gguf",
  "runtime": "llama.cpp",
  "stage_index": ${stage_index:-0},
  "stage_start": ${stage_start:-0},
  "stage_end": ${stage_end:-0},
  "output_file": "$output_file",
  "selected_bytes": 16,
  "bundle_bytes": 17,
  "loadable_gguf": false,
  "guardrail": "fake shard bundle for daemon decode-loop smoke"
}
JSON
    ;;
  source-decode)
    output_file="$(arg_value --output-file "$@")"
    step="${CMESH_STAGE_STEP:-1}"
    python3 - "$output_file" "$step" <<'PY'
import struct, sys
step = float(sys.argv[2])
with open(sys.argv[1], "wb") as f:
    f.write(struct.pack("<ffff", step, step + 0.25, step + 0.5, step + 0.75))
PY
    cat <<JSON
{
  "kind": "cmesh.llamacpp_stage_source_decode",
  "status": "executed",
  "runtime": "llama.cpp",
  "stage_index": ${stage_index:-0},
  "stage_start": ${stage_start:-0},
  "stage_end": ${stage_end:-0},
  "input_tensor": {"dtype": "tokens", "shape": [1, 1], "bytes": 4},
  "output_tensor": {"dtype": "f32", "shape": [1, 1, 4], "bytes": 16, "path": "$output_file"},
  "decode_status": 0
}
JSON
    ;;
  terminal-decode)
    step="${CMESH_STAGE_STEP:-1}"
    final_value="false"
    if [ "$step" -ge 3 ]; then
      final_value="true"
    fi
    token_id="$((7000 + step))"
    token_text=" token-${step}"
    output_text=""
    for i in $(seq 1 "$step"); do
      output_text="${output_text} token-${i}"
    done
    tokens_json="$(python3 - "$step" <<'PY'
import json, sys
step = int(sys.argv[1])
print(json.dumps([7000 + i for i in range(1, step + 1)]))
PY
)"
    cat <<JSON
{
  "kind": "cmesh.llamacpp_stage_terminal_decode",
  "status": "executed",
  "runtime": "llama.cpp",
  "stage_index": ${stage_index:-1},
  "stage_start": ${stage_start:-1},
  "stage_end": ${stage_end:-1},
  "input_tensor": {"dtype": "$(arg_value --dtype "$@")", "shape": [1, 1, 4], "bytes": 16},
  "logits": {"dtype": "f32", "shape": [1, 4], "bytes": 16},
  "next_token_id": ${token_id},
  "next_token_text": "${token_text}",
  "tokens": ${tokens_json},
  "output": "${output_text}",
  "final": ${final_value},
  "decode_status": 0
}
JSON
    ;;
  *)
    echo "unsupported fake command $command" >&2
    exit 2
    ;;
esac
SH
chmod +x "$FAKE_RUNNER"
touch "$MODEL_PATH"

free_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

if [[ "$MANAGER_PORT" == "0" ]]; then
  MANAGER_PORT="$(free_port)"
fi
if [[ "$DAEMON_PORT" == "0" ]]; then
  DAEMON_PORT="$(free_port)"
fi

MANAGER="http://127.0.0.1:${MANAGER_PORT}"
DAEMON="http://127.0.0.1:${DAEMON_PORT}"

"$BIN" stage-runner daemon --addr "127.0.0.1:${DAEMON_PORT}" --session-dir "$RUN_DIR/stage-sessions" >"$RUN_DIR/stage-daemon.log" 2>&1 &
DAEMON_PID=$!
"$BIN" manager start --memory --addr "127.0.0.1:${MANAGER_PORT}" --join-token smoke-token --cdip-auto-advance=false >"$RUN_DIR/manager.log" 2>&1 &
MANAGER_PID=$!
trap 'kill "$MANAGER_PID" "$DAEMON_PID" >/dev/null 2>&1 || true' EXIT

for _ in $(seq 1 80); do
  if curl -fsS "$MANAGER/health" >/dev/null 2>&1 && curl -fsS "$DAEMON/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

join_worker() {
  local name="$1"
  local response node_id
  response="$(curl -fsS -X POST "$MANAGER/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"${name}\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":8,\"cores_allowed\":4},\"memory\":{\"total_bytes\":34359738368,\"allowed_bytes\":21474836480},\"storage\":{\"total_bytes\":107374182400,\"allowed_bytes\":53687091200,\"free_bytes\":85899345920},\"models\":[{\"id\":\"${MODEL_ID}\",\"runtime\":\"llama.cpp\",\"path\":\"${MODEL_PATH}\",\"ready\":true}],\"runtimes\":[{\"name\":\"llama.cpp\",\"ready\":true,\"capabilities\":[\"logical-stage-runtime\",\"llama.cpp-stage-runtime\",\"llama.cpp-stage-runner\"],\"stage_runtimes\":[{\"name\":\"cmesh-stage-daemon\",\"ready\":true,\"endpoint\":\"${DAEMON}\",\"protocol\":\"cdip.stage-session-v1\"}]}]},\"join_token\":\"smoke-token\"}")"
  node_id="$(jq -r '.node_id' <<<"$response")"
  jq -r '.node_auth_token' <<<"$response" >"$RUN_DIR/node-token-$node_id"
  printf '%s\n' "$node_id"
}

NODE_A="$(join_worker daemon-loop-stage-a)"
NODE_B="$(join_worker daemon-loop-stage-b)"

run_worker_once() {
  local node_id="$1"
  local cache_dir="$RUN_DIR/cache-$node_id"
  local node_auth_token
  node_auth_token="$(cat "$RUN_DIR/node-token-$node_id")"
  "$BIN" worker poll-once \
    --manager "$MANAGER" \
    --node-id "$node_id" \
    --node-auth-token "$node_auth_token" \
    --cache-dir "$cache_dir" \
    --model-id "$MODEL_ID" \
    --model-path "$MODEL_PATH" \
    --runtime "llama.cpp" \
    --cpu 4 \
    --memory-gb 20 \
    --disk-gb 50 >"$RUN_DIR/poll-$node_id-$(date -u +%s%N).log" 2>&1
}

created="$(jq -n \
  --arg prompt "daemon decode-loop smoke" \
  --arg runner "$FAKE_RUNNER" \
  --arg model_path "$MODEL_PATH" \
  --arg work_dir "$WORK_DIR" \
  --argjson timeout_ms "$TIMEOUT_MS" \
  '{prompt:$prompt,max_tokens:3,temperature:"0.1",stage_runner_bin:$runner,model_path:$model_path,work_dir:$work_dir,timeout_ms:$timeout_ms}' \
  | curl -fsS -X POST "$MANAGER/v1/models/$MODEL_ID/distributed-generate" -H 'Content-Type: application/json' -d @-)"
echo "$created" >"$RUN_DIR/distributed-generate.json"
parent_id="$(jq -r '.job.id' "$RUN_DIR/distributed-generate.json")"
jq -c '.stage_jobs[] | {id, assigned_to, index:.cdip_stage_index, session:(.input | fromjson | .stage_session_id)}' "$RUN_DIR/distributed-generate.json" >"$RUN_DIR/stage-workers.jsonl"
source_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].assigned_to' "$RUN_DIR/distributed-generate.json")"
terminal_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].assigned_to' "$RUN_DIR/distributed-generate.json")"
source_session="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].input | fromjson | .stage_session_id' "$RUN_DIR/distributed-generate.json")"
terminal_session="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].input | fromjson | .stage_session_id' "$RUN_DIR/distributed-generate.json")"

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prepare" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/prepare.json"
while IFS= read -r stage_worker; do
  run_worker_once "$(jq -r '.assigned_to' <<<"$stage_worker")"
done <"$RUN_DIR/stage-workers.jsonl"

curl -fsS "$DAEMON/v1/sessions/${source_session}" >"$RUN_DIR/source-session-after-prepare.json"
curl -fsS "$DAEMON/v1/sessions/${terminal_session}" >"$RUN_DIR/terminal-session-after-prepare.json"
jq -e '.session.persistent_kv_in_memory == true and .decode_steps == 0' "$RUN_DIR/source-session-after-prepare.json" >/dev/null
jq -e '.session.persistent_kv_in_memory == true and .decode_steps == 0' "$RUN_DIR/terminal-session-after-prepare.json" >/dev/null

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prefill" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/prefill.json"
curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/decode-loop" -H 'Content-Type: application/json' -d '{"mode":"dispatch","step":1,"max_tokens":3}' >"$RUN_DIR/decode-loop-step-1.json"

for step in 1 2 3; do
  run_worker_once "$source_node"
  run_worker_once "$terminal_node"
  curl -fsS "$DAEMON/v1/sessions/${source_session}" >"$RUN_DIR/source-session-step-${step}.json"
  curl -fsS "$DAEMON/v1/sessions/${terminal_session}" >"$RUN_DIR/terminal-session-step-${step}.json"
  jq -e --argjson step "$step" '.decode_steps == $step and .last_step == $step and .last_stage_command == "source_decode" and .last_payload_bytes > 0 and .session.persistent_kv_in_memory == true' "$RUN_DIR/source-session-step-${step}.json" >/dev/null
  jq -e --argjson step "$step" '.decode_steps == $step and .last_step == $step and .last_stage_command == "terminal_decode" and .last_payload_bytes > 0 and .session.persistent_kv_in_memory == true' "$RUN_DIR/terminal-session-step-${step}.json" >/dev/null
done

curl -fsS "$MANAGER/v1/jobs/$parent_id" >"$RUN_DIR/parent.json"
jq -e '
  .status == "succeeded" and
  (.result | fromjson | .kind == "cdip.distributed_terminal_result") and
  (.result | fromjson | .token_count == 3) and
  (.result | fromjson | .output == " token-1 token-2 token-3")
' "$RUN_DIR/parent.json" >/dev/null

curl -fsS "$DAEMON/health" >"$RUN_DIR/stage-daemon-health-final.json"
jq -e '.session_count == 2 and .protocol == "cdip.stage-session-v1"' "$RUN_DIR/stage-daemon-health-final.json" >/dev/null

cat >"$RUN_DIR/summary.json" <<JSON
{
  "status": "passed",
  "parent_job_id": "$parent_id",
  "source_session": "$source_session",
  "terminal_session": "$terminal_session",
  "decode_steps": 3,
  "manager": "$MANAGER",
  "daemon": "$DAEMON"
}
JSON

echo "PASS: CDIP daemon decode-loop smoke succeeded"
echo "Evidence: $RUN_DIR"
