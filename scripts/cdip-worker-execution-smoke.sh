#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_CDIP_WORKER_EXECUTION_SMOKE_DIR:-/tmp/cmesh-cdip-worker-execution-smoke-$(date -u +%Y%m%d%H%M%S)}"
PORT="${CMESH_CDIP_WORKER_EXECUTION_SMOKE_PORT:-0}"
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
  "model_name": "fake-smoke-model",
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
  "guardrail": "fake stage runner for worker execution smoke"
}
JSON
    ;;
  source-decode)
    output_file="$(arg_value --output-file "$@")"
    python3 - "$output_file" <<'PY'
import struct, sys
with open(sys.argv[1], "wb") as f:
    f.write(struct.pack("<ffff", 0.25, 0.5, 0.75, 1.0))
PY
    cat <<JSON
{
  "kind": "cmesh.llamacpp_stage_decode",
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
    final_value="true"
    token_id="4242"
    token_text=" distributed-ok"
    tokens_json="[4242]"
    output_text=" distributed-ok"
    if [ "$step" = "2" ]; then
      final_value="false"
      token_id="4241"
      token_text=" partial"
      tokens_json="[4241]"
      output_text=" partial"
    fi
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

if [[ "$PORT" == "0" ]]; then
  PORT="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"
fi

MANAGER="http://127.0.0.1:${PORT}"
"$BIN" manager start --memory --addr "127.0.0.1:${PORT}" --join-token smoke-token --cdip-auto-advance=false >"$RUN_DIR/manager.log" 2>&1 &
MANAGER_PID=$!
trap 'kill "$MANAGER_PID" >/dev/null 2>&1 || true' EXIT

for _ in $(seq 1 80); do
  if curl -fsS "$MANAGER/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

join_worker() {
  local name="$1"
  local response node_id
  response="$(curl -fsS -X POST "$MANAGER/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"${name}\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":8,\"cores_allowed\":4},\"memory\":{\"total_bytes\":34359738368,\"allowed_bytes\":21474836480},\"storage\":{\"total_bytes\":107374182400,\"allowed_bytes\":53687091200,\"free_bytes\":85899345920},\"models\":[{\"id\":\"${MODEL_ID}\",\"runtime\":\"llama.cpp\",\"path\":\"${MODEL_PATH}\",\"ready\":true}],\"runtimes\":[{\"name\":\"llama.cpp\",\"ready\":true,\"capabilities\":[\"logical-stage-runtime\",\"llama.cpp-stage-runner\"]}]},\"join_token\":\"smoke-token\"}")"
  node_id="$(jq -r '.node_id' <<<"$response")"
  jq -r '.node_auth_token' <<<"$response" >"$RUN_DIR/node-token-$node_id"
  printf '%s\n' "$node_id"
}

NODE_A="$(join_worker worker-exec-stage-a)"
NODE_B="$(join_worker worker-exec-stage-b)"

created="$(jq -n \
  --arg prompt "worker execution smoke" \
  --arg runner "$FAKE_RUNNER" \
  --arg model_path "$MODEL_PATH" \
  --arg work_dir "$WORK_DIR" \
  --argjson timeout_ms "$TIMEOUT_MS" \
  '{prompt:$prompt,max_tokens:1,temperature:"0.1",stage_runner_bin:$runner,model_path:$model_path,work_dir:$work_dir,timeout_ms:$timeout_ms}' \
  | curl -fsS -X POST "$MANAGER/v1/models/$MODEL_ID/distributed-generate" -H 'Content-Type: application/json' -d @-)"
echo "$created" >"$RUN_DIR/distributed-generate.json"
parent_id="$(jq -r '.job.id' "$RUN_DIR/distributed-generate.json")"
jq -c '.stage_jobs[] | {id, assigned_to, index:.cdip_stage_index}' "$RUN_DIR/distributed-generate.json" >"$RUN_DIR/stage-workers.jsonl"

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
    --disk-gb 50 >"$RUN_DIR/poll-$node_id.log" 2>&1
}

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prepare" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/prepare.json"
while IFS= read -r stage_worker; do
  run_worker_once "$(jq -r '.assigned_to' <<<"$stage_worker")"
done <"$RUN_DIR/stage-workers.jsonl"

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prefill" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/prefill.json"
curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/decode" -H 'Content-Type: application/json' -d '{"mode":"relay_decode","step":1}' >"$RUN_DIR/decode.json"

source_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].assigned_to' "$RUN_DIR/decode.json")"
terminal_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].assigned_to' "$RUN_DIR/decode.json")"
run_worker_once "$source_node"
run_worker_once "$terminal_node"

curl -fsS "$MANAGER/v1/jobs/$parent_id" >"$RUN_DIR/parent.json"
jq -e '.status == "succeeded" and (.result | fromjson | .kind == "cdip.distributed_terminal_result") and (.result | fromjson | .output == " distributed-ok")' "$RUN_DIR/parent.json" >/dev/null

curl -fsS "$MANAGER/v1/jobs" >"$RUN_DIR/jobs.json"
jq -e --arg parent "$parent_id" '[.jobs[] | select(.type == "model.generate.distributed.stage" and .cdip_parent_job_id == $parent)] | length >= 2' "$RUN_DIR/jobs.json" >/dev/null

dispatch_created="$(jq -n \
  --arg prompt "worker execution decode-loop dispatch smoke" \
  --arg runner "$FAKE_RUNNER" \
  --arg model_path "$MODEL_PATH" \
  --arg work_dir "$WORK_DIR-dispatch" \
  --argjson timeout_ms "$TIMEOUT_MS" \
  '{prompt:$prompt,max_tokens:3,temperature:"0.1",stage_runner_bin:$runner,model_path:$model_path,work_dir:$work_dir,timeout_ms:$timeout_ms}' \
  | curl -fsS -X POST "$MANAGER/v1/models/$MODEL_ID/distributed-generate" -H 'Content-Type: application/json' -d @-)"
echo "$dispatch_created" >"$RUN_DIR/dispatch-distributed-generate.json"
dispatch_parent_id="$(jq -r '.job.id' "$RUN_DIR/dispatch-distributed-generate.json")"
jq -c '.stage_jobs[] | {id, assigned_to, index:.cdip_stage_index}' "$RUN_DIR/dispatch-distributed-generate.json" >"$RUN_DIR/dispatch-stage-workers.jsonl"

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${dispatch_parent_id}/prepare" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/dispatch-prepare.json"
while IFS= read -r stage_worker; do
  run_worker_once "$(jq -r '.assigned_to' <<<"$stage_worker")"
done <"$RUN_DIR/dispatch-stage-workers.jsonl"

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${dispatch_parent_id}/prefill" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/dispatch-prefill.json"
curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${dispatch_parent_id}/decode-loop" -H 'Content-Type: application/json' -d '{"mode":"dispatch","step":2,"max_tokens":3}' >"$RUN_DIR/dispatch-decode-loop.json"
jq -e '
  .parent_job.id == "'"${dispatch_parent_id}"'" and
  .parent_job.status != "succeeded" and
  .trace.mode == "worker-dispatch" and
  .trace.kv_cache_key == ("cdip-session-" + "'"${dispatch_parent_id}"'" + ":kv") and
  (.messages | length == 2) and
  (.messages | all(.step == 2)) and
  (.stage_jobs | all(.status == "scheduled" and .cdip_state == "decode"))
' "$RUN_DIR/dispatch-decode-loop.json" >/dev/null

dispatch_source_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].assigned_to' "$RUN_DIR/dispatch-decode-loop.json")"
dispatch_terminal_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].assigned_to' "$RUN_DIR/dispatch-decode-loop.json")"
run_worker_once "$dispatch_source_node"
run_worker_once "$dispatch_terminal_node"

curl -fsS "$MANAGER/v1/jobs/$dispatch_parent_id" >"$RUN_DIR/dispatch-parent.json"
jq -e '
  .status != "succeeded"
' "$RUN_DIR/dispatch-parent.json" >/dev/null
curl -fsS "$MANAGER/v1/jobs" >"$RUN_DIR/dispatch-after-partial-jobs.json"
jq -e --arg parent "$dispatch_parent_id" --arg kv "cdip-session-${dispatch_parent_id}:kv" '
  [.jobs[] | select(.type == "model.generate.distributed.stage" and .cdip_parent_job_id == $parent) | (.input | fromjson)]
  | length == 2
  and all(.step == 3 and .kv_cache_key == $kv)
  and any(.stage_command == "source_decode")
  and any(.stage_command == "terminal_decode")
' "$RUN_DIR/dispatch-after-partial-jobs.json" >/dev/null

run_worker_once "$dispatch_source_node"
run_worker_once "$dispatch_terminal_node"

curl -fsS "$MANAGER/v1/jobs/$dispatch_parent_id" >"$RUN_DIR/dispatch-parent-final.json"
jq -e '
  .status == "succeeded" and
  (.result | fromjson | .kind == "cdip.distributed_terminal_result") and
  (.result | fromjson | .output == " distributed-ok") and
  (.result | fromjson | .final == true) and
  (.result | fromjson | .step == 3) and
  (.result | fromjson | .kv_cache_key == ("cdip-session-" + "'"${dispatch_parent_id}"'" + ":kv"))
' "$RUN_DIR/dispatch-parent-final.json" >/dev/null

curl -fsS "$MANAGER/v1/jobs" >"$RUN_DIR/dispatch-jobs.json"
jq -e --arg parent "$dispatch_parent_id" --arg kv "cdip-session-${dispatch_parent_id}:kv" '
  [.jobs[] | select(.type == "model.generate.distributed.stage" and .cdip_parent_job_id == $parent) | (.input | fromjson)]
  | length == 2
  and all(.step == 3 and .kv_cache_key == $kv)
  and any(.stage_command == "source_decode")
  and any(.stage_command == "terminal_decode")
' "$RUN_DIR/dispatch-jobs.json" >/dev/null

echo "PASS: CDIP worker execution smoke succeeded"
echo "Evidence: $RUN_DIR"
