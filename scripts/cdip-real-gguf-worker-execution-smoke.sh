#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! MODEL_PATH="$("$ROOT_DIR/scripts/ensure-gguf-fixture.sh")"; then
  echo "SKIP: set CMESH_GGUF_MODEL_PATH=/path/to/model.gguf or CMESH_DOWNLOAD_GGUF_FIXTURE=1 to run real GGUF worker execution smoke"
  exit 0
fi
RUN_DIR="${CMESH_CDIP_REAL_GGUF_WORKER_SMOKE_DIR:-/tmp/cmesh-cdip-real-gguf-worker-smoke-$(date -u +%Y%m%d%H%M%S)}"
PORT="${CMESH_CDIP_REAL_GGUF_WORKER_SMOKE_PORT:-0}"
DAEMON_A_PORT="${CMESH_CDIP_REAL_GGUF_WORKER_DAEMON_A_PORT:-${CMESH_CDIP_REAL_GGUF_WORKER_DAEMON_PORT:-0}}"
DAEMON_B_PORT="${CMESH_CDIP_REAL_GGUF_WORKER_DAEMON_B_PORT:-0}"
MODEL_ID="${CMESH_MODEL_ID:-qwen2.5-0.5b-instruct-q4-k-m}"
STAGE_WORK_DIR="${CMESH_STAGE_WORK_DIR:-$RUN_DIR/stage-work}"
TIMEOUT_MS="${CMESH_STAGE_TIMEOUT_MS:-60000}"
PROMPT="${CMESH_STAGE_PIPELINE_PROMPT:-hello from cmesh distributed worker execution}"
LLAMA_WORK_DIR="${WORK_DIR:-/tmp/cmesh-llama-stage-runner}"

mkdir -p "$RUN_DIR"

BIN="$RUN_DIR/cmesh"
go build -o "$BIN" "$ROOT_DIR/cmd/cmesh"

WORK_DIR="$LLAMA_WORK_DIR" "$ROOT_DIR/scripts/prepare-llamacpp-stage-runner-worktree.sh" >"$RUN_DIR/prepare-runner.log"
RUNNER="$LLAMA_WORK_DIR/src/build-cmesh-stage/bin/cmesh-stage-runner"
if [[ ! -x "$RUNNER" ]]; then
  echo "FAIL: stage runner was not built at $RUNNER" >&2
  exit 1
fi

"$RUNNER" \
  --command prepare \
  --model "$MODEL_PATH" \
  --stage-start 0 \
  --stage-end 0 \
  --stage-index 0 \
  >"$RUN_DIR/model-prepare.json"

N_LAYER="$(jq -r '.n_layer' "$RUN_DIR/model-prepare.json")"
if [[ -z "$N_LAYER" || "$N_LAYER" == "null" || "$N_LAYER" -lt 2 ]]; then
  echo "FAIL: expected model with at least 2 layers, got n_layer=$N_LAYER" >&2
  exit 1
fi

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
if [[ "$DAEMON_A_PORT" == "0" ]]; then
  DAEMON_A_PORT="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"
fi
if [[ "$DAEMON_B_PORT" == "0" ]]; then
  DAEMON_B_PORT="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"
fi

MANAGER="http://127.0.0.1:${PORT}"
DAEMON_A="http://127.0.0.1:${DAEMON_A_PORT}"
DAEMON_B="http://127.0.0.1:${DAEMON_B_PORT}"
"$BIN" stage-runner daemon \
  --addr "127.0.0.1:${DAEMON_A_PORT}" \
  --session-dir "$RUN_DIR/stage-sessions-a" \
  --backend llama.cpp-resident \
  --runner-bin "$RUNNER" >"$RUN_DIR/stage-daemon-a.log" 2>&1 &
DAEMON_A_PID=$!
"$BIN" stage-runner daemon \
  --addr "127.0.0.1:${DAEMON_B_PORT}" \
  --session-dir "$RUN_DIR/stage-sessions-b" \
  --backend llama.cpp-resident \
  --runner-bin "$RUNNER" >"$RUN_DIR/stage-daemon-b.log" 2>&1 &
DAEMON_B_PID=$!
"$BIN" manager start --memory --addr "127.0.0.1:${PORT}" --join-token smoke-token --cdip-auto-advance=false >"$RUN_DIR/manager.log" 2>&1 &
MANAGER_PID=$!
trap 'kill "$MANAGER_PID" "$DAEMON_A_PID" "$DAEMON_B_PID" >/dev/null 2>&1 || true' EXIT

for _ in $(seq 1 80); do
  if curl -fsS "$MANAGER/health" >/dev/null 2>&1 && curl -fsS "$DAEMON_A/health" >/dev/null 2>&1 && curl -fsS "$DAEMON_B/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

join_worker() {
  local name="$1"
  local daemon="$2"
  local response node_id
  response="$(curl -fsS -X POST "$MANAGER/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"${name}\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":8,\"cores_allowed\":4},\"memory\":{\"total_bytes\":34359738368,\"allowed_bytes\":21474836480},\"storage\":{\"total_bytes\":107374182400,\"allowed_bytes\":53687091200,\"free_bytes\":85899345920},\"models\":[{\"id\":\"${MODEL_ID}\",\"runtime\":\"llama.cpp\",\"path\":\"${MODEL_PATH}\",\"layers\":${N_LAYER},\"ready\":true}],\"runtimes\":[{\"name\":\"llama.cpp\",\"ready\":true,\"capabilities\":[\"logical-stage-runtime\",\"llama.cpp-stage-runner\"],\"stage_runtimes\":[{\"name\":\"cmesh-stage-daemon\",\"ready\":true,\"endpoint\":\"${daemon}\",\"protocol\":\"cdip.stage-session-v1\"}]}]},\"join_token\":\"smoke-token\"}")"
  node_id="$(jq -r '.node_id' <<<"$response")"
  jq -r '.node_auth_token' <<<"$response" >"$RUN_DIR/node-token-$node_id"
  printf '%s\n' "$node_id"
}

NODE_A="$(join_worker real-gguf-stage-a "$DAEMON_A")"
NODE_B="$(join_worker real-gguf-stage-b "$DAEMON_B")"

created="$(jq -n \
  --arg prompt "$PROMPT" \
  --arg runner "$RUNNER" \
  --arg model_path "$MODEL_PATH" \
  --arg work_dir "$STAGE_WORK_DIR" \
  --argjson timeout_ms "$TIMEOUT_MS" \
  '{prompt:$prompt,max_tokens:1,temperature:"0.1",stage_runner_bin:$runner,model_path:$model_path,work_dir:$work_dir,timeout_ms:$timeout_ms}' \
  | curl -fsS -X POST "$MANAGER/v1/models/$MODEL_ID/distributed-generate" -H 'Content-Type: application/json' -d @-)"
echo "$created" >"$RUN_DIR/distributed-generate.json"
parent_id="$(jq -r '.job.id' "$RUN_DIR/distributed-generate.json")"
jq -e --argjson total_layers "$N_LAYER" '.plan.total_layers == $total_layers' "$RUN_DIR/distributed-generate.json" >/dev/null
jq -c '.stage_jobs[] | {id, assigned_to, index:.cdip_stage_index}' "$RUN_DIR/distributed-generate.json" >"$RUN_DIR/stage-workers.jsonl"
source_session="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].input | fromjson | .stage_session_id' "$RUN_DIR/distributed-generate.json")"
terminal_session="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].input | fromjson | .stage_session_id' "$RUN_DIR/distributed-generate.json")"

jq -e --arg daemon_a "$DAEMON_A" --arg daemon_b "$DAEMON_B" '
  (.stage_jobs | sort_by(.cdip_stage_index) | .[0].input | fromjson | .stage_daemon_url) == $daemon_a
  and (.stage_jobs | sort_by(.cdip_stage_index) | .[-1].input | fromjson | .stage_daemon_url) == $daemon_b
  and $daemon_a != $daemon_b
' "$RUN_DIR/distributed-generate.json" >/dev/null

daemon_for_node() {
  local node_id="$1"
  if [[ "$node_id" == "$NODE_A" ]]; then
    printf '%s\n' "$DAEMON_A"
    return
  fi
  if [[ "$node_id" == "$NODE_B" ]]; then
    printf '%s\n' "$DAEMON_B"
    return
  fi
  echo "FAIL: no stage daemon mapping for node $node_id" >&2
  exit 1
}

run_worker_once() {
  local node_id="$1"
  local force_final="${2:-}"
  local cache_dir="$RUN_DIR/cache-$node_id"
  local daemon_url
  local node_auth_token
  daemon_url="$(daemon_for_node "$node_id")"
  node_auth_token="$(cat "$RUN_DIR/node-token-$node_id")"
  if [[ -n "$force_final" ]]; then
    CMESH_TERMINAL_FORCE_FINAL="$force_final" "$BIN" worker poll-once \
      --manager "$MANAGER" \
      --node-id "$node_id" \
      --node-auth-token "$node_auth_token" \
      --cache-dir "$cache_dir" \
      --model-id "$MODEL_ID" \
      --model-path "$MODEL_PATH" \
      --runtime "llama.cpp" \
      --stage-daemon-url "$daemon_url" \
      --cpu 4 \
      --memory-gb 20 \
      --disk-gb 50 >"$RUN_DIR/poll-$node_id.log" 2>&1
    return
  fi
  "$BIN" worker poll-once \
    --manager "$MANAGER" \
    --node-id "$node_id" \
    --node-auth-token "$node_auth_token" \
    --cache-dir "$cache_dir" \
    --model-id "$MODEL_ID" \
    --model-path "$MODEL_PATH" \
    --runtime "llama.cpp" \
    --stage-daemon-url "$daemon_url" \
    --cpu 4 \
    --memory-gb 20 \
    --disk-gb 50 >"$RUN_DIR/poll-$node_id.log" 2>&1
}

extract_stage_runner_json() {
  python3 - "$1" <<'PY'
import json
import sys

path = sys.argv[1]
text = open(path, encoding="utf-8", errors="replace").read()
for index, char in enumerate(text):
    if char != "{":
        continue
    try:
        parsed = json.loads(text[index:])
    except json.JSONDecodeError:
        continue
    print(json.dumps(parsed))
    raise SystemExit(0)
raise SystemExit(f"no JSON report found in {path}")
PY
}

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prepare" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/prepare.json"
while IFS= read -r stage_worker; do
  run_worker_once "$(jq -r '.assigned_to' <<<"$stage_worker")"
done <"$RUN_DIR/stage-workers.jsonl"
curl -fsS "$DAEMON_A/v1/sessions/${source_session}" >"$RUN_DIR/source-session-after-prepare.json"
curl -fsS "$DAEMON_B/v1/sessions/${terminal_session}" >"$RUN_DIR/terminal-session-after-prepare.json"
if curl -fsS "$DAEMON_B/v1/sessions/${source_session}" >/dev/null 2>&1; then
  echo "FAIL: source stage session was visible on terminal daemon" >&2
  exit 1
fi
if curl -fsS "$DAEMON_A/v1/sessions/${terminal_session}" >/dev/null 2>&1; then
  echo "FAIL: terminal stage session was visible on source daemon" >&2
  exit 1
fi
jq -e '.session.persistent_kv_in_memory == true and .decode_steps == 0' "$RUN_DIR/source-session-after-prepare.json" >/dev/null
jq -e '.session.persistent_kv_in_memory == true and .decode_steps == 0' "$RUN_DIR/terminal-session-after-prepare.json" >/dev/null

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prefill" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/prefill.json"
curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/decode" -H 'Content-Type: application/json' -d '{"mode":"relay_decode","step":1}' >"$RUN_DIR/decode.json"

source_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].assigned_to' "$RUN_DIR/decode.json")"
terminal_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].assigned_to' "$RUN_DIR/decode.json")"
run_worker_once "$source_node"
run_worker_once "$terminal_node"

curl -fsS "$MANAGER/v1/jobs/$parent_id" >"$RUN_DIR/parent.json"
jq -e '.status == "succeeded" and (.result | fromjson | .kind == "cdip.distributed_terminal_result") and ((.result | fromjson | .tokens | length) >= 1)' "$RUN_DIR/parent.json" >/dev/null
curl -fsS "$MANAGER/v1/jobs" >"$RUN_DIR/initial-jobs.json"
jq -e --arg parent "$parent_id" '
  [.jobs[] | select(.type == "model.generate.distributed.stage" and .cdip_parent_job_id == $parent and (.result | fromjson? | .worker_result.kind) == "cdip.stage_terminal_decode")]
  | length >= 1
  and all((.result | fromjson | .worker_result.runner_mode) == "llama.cpp-stage-daemon")
  and all((.result | fromjson | .worker_result.stage_daemon_decode.next_token_id) != null)
' "$RUN_DIR/initial-jobs.json" >/dev/null
curl -fsS "$DAEMON_B/v1/sessions/${terminal_session}" >"$RUN_DIR/terminal-session-after-initial.json"
jq -e '.last_stage_command == "terminal_decode" and .last_payload_bytes > 0 and .session.persistent_kv_in_memory == true' "$RUN_DIR/terminal-session-after-initial.json" >/dev/null

dispatch_created="$(jq -n \
  --arg prompt "$PROMPT" \
  --arg runner "$RUNNER" \
  --arg model_path "$MODEL_PATH" \
  --arg work_dir "$STAGE_WORK_DIR-dispatch" \
  --argjson timeout_ms "$TIMEOUT_MS" \
  '{prompt:$prompt,max_tokens:3,temperature:"0.1",stage_runner_bin:$runner,model_path:$model_path,work_dir:$work_dir,timeout_ms:$timeout_ms}' \
  | curl -fsS -X POST "$MANAGER/v1/models/$MODEL_ID/distributed-generate" -H 'Content-Type: application/json' -d @-)"
echo "$dispatch_created" >"$RUN_DIR/dispatch-distributed-generate.json"
dispatch_parent_id="$(jq -r '.job.id' "$RUN_DIR/dispatch-distributed-generate.json")"
jq -e --argjson total_layers "$N_LAYER" '.plan.total_layers == $total_layers' "$RUN_DIR/dispatch-distributed-generate.json" >/dev/null
jq -c '.stage_jobs[] | {id, assigned_to, index:.cdip_stage_index}' "$RUN_DIR/dispatch-distributed-generate.json" >"$RUN_DIR/dispatch-stage-workers.jsonl"
dispatch_source_session="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].input | fromjson | .stage_session_id' "$RUN_DIR/dispatch-distributed-generate.json")"
dispatch_terminal_session="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].input | fromjson | .stage_session_id' "$RUN_DIR/dispatch-distributed-generate.json")"

jq -e --arg daemon_a "$DAEMON_A" --arg daemon_b "$DAEMON_B" '
  (.stage_jobs | sort_by(.cdip_stage_index) | .[0].input | fromjson | .stage_daemon_url) == $daemon_a
  and (.stage_jobs | sort_by(.cdip_stage_index) | .[-1].input | fromjson | .stage_daemon_url) == $daemon_b
  and $daemon_a != $daemon_b
' "$RUN_DIR/dispatch-distributed-generate.json" >/dev/null

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${dispatch_parent_id}/prepare" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/dispatch-prepare.json"
while IFS= read -r stage_worker; do
  run_worker_once "$(jq -r '.assigned_to' <<<"$stage_worker")"
done <"$RUN_DIR/dispatch-stage-workers.jsonl"
curl -fsS "$DAEMON_A/v1/sessions/${dispatch_source_session}" >"$RUN_DIR/dispatch-source-session-after-prepare.json"
curl -fsS "$DAEMON_B/v1/sessions/${dispatch_terminal_session}" >"$RUN_DIR/dispatch-terminal-session-after-prepare.json"
if curl -fsS "$DAEMON_B/v1/sessions/${dispatch_source_session}" >/dev/null 2>&1; then
  echo "FAIL: dispatch source stage session was visible on terminal daemon" >&2
  exit 1
fi
if curl -fsS "$DAEMON_A/v1/sessions/${dispatch_terminal_session}" >/dev/null 2>&1; then
  echo "FAIL: dispatch terminal stage session was visible on source daemon" >&2
  exit 1
fi
jq -e '.session.persistent_kv_in_memory == true and .decode_steps == 0' "$RUN_DIR/dispatch-source-session-after-prepare.json" >/dev/null
jq -e '.session.persistent_kv_in_memory == true and .decode_steps == 0' "$RUN_DIR/dispatch-terminal-session-after-prepare.json" >/dev/null

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${dispatch_parent_id}/prefill" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/dispatch-prefill.json"
curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${dispatch_parent_id}/decode-loop" -H 'Content-Type: application/json' -d '{"mode":"dispatch","step":2,"max_tokens":3}' >"$RUN_DIR/dispatch-decode-loop.json"
jq -e '
  .parent_job.id == "'"${dispatch_parent_id}"'" and
  .parent_job.status != "succeeded" and
  .trace.mode == "worker-dispatch" and
  .trace.kv_cache_key == ("cdip-session-" + "'"${dispatch_parent_id}"'" + ":kv") and
  (.messages | length) >= 2 and
  (.messages | all(.step == 2)) and
  (.stage_jobs | all(.status == "scheduled" and .cdip_state == "decode"))
' "$RUN_DIR/dispatch-decode-loop.json" >/dev/null

dispatch_source_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].assigned_to' "$RUN_DIR/dispatch-decode-loop.json")"
dispatch_terminal_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].assigned_to' "$RUN_DIR/dispatch-decode-loop.json")"
run_worker_once "$dispatch_source_node"
run_worker_once "$dispatch_terminal_node" false

curl -fsS "$MANAGER/v1/jobs/$dispatch_parent_id" >"$RUN_DIR/dispatch-parent.json"
jq -e '.status != "succeeded"' "$RUN_DIR/dispatch-parent.json" >/dev/null
curl -fsS "$MANAGER/v1/jobs" >"$RUN_DIR/dispatch-after-partial-jobs.json"
jq -e --arg parent "$dispatch_parent_id" --arg kv "cdip-session-${dispatch_parent_id}:kv" '
  [.jobs[] | select(.type == "model.generate.distributed.stage" and .cdip_parent_job_id == $parent) | (.input | fromjson)]
  | length >= 2
  and all(.step == 3 and .kv_cache_key == $kv)
  and any(.stage_command == "source_decode" and .previous_token_id != null)
  and any(.stage_command == "terminal_decode")
' "$RUN_DIR/dispatch-after-partial-jobs.json" >/dev/null

run_worker_once "$dispatch_source_node"
run_worker_once "$dispatch_terminal_node"

curl -fsS "$MANAGER/v1/jobs/$dispatch_parent_id" >"$RUN_DIR/dispatch-parent-final.json"
jq -e '.status == "succeeded" and (.result | fromjson | .kind == "cdip.distributed_terminal_result") and ((.result | fromjson | .tokens | length) >= 1) and (.result | fromjson | .step == 3)' "$RUN_DIR/dispatch-parent-final.json" >/dev/null
curl -fsS "$DAEMON_A/v1/sessions/${dispatch_source_session}" >"$RUN_DIR/dispatch-source-session-final.json"
curl -fsS "$DAEMON_B/v1/sessions/${dispatch_terminal_session}" >"$RUN_DIR/dispatch-terminal-session-final.json"
jq -e '.decode_steps >= 2 and .last_step == 3 and .last_stage_command == "source_decode" and .last_payload_bytes > 0 and .session.persistent_kv_in_memory == true' "$RUN_DIR/dispatch-source-session-final.json" >/dev/null
jq -e '.decode_steps >= 2 and .last_step == 3 and .last_stage_command == "terminal_decode" and .last_payload_bytes > 0 and .session.persistent_kv_in_memory == true' "$RUN_DIR/dispatch-terminal-session-final.json" >/dev/null
curl -fsS "$MANAGER/v1/jobs" >"$RUN_DIR/dispatch-jobs.json"
jq -e --arg parent "$dispatch_parent_id" '
  [.jobs[] | select(.type == "model.generate.distributed.stage" and .cdip_parent_job_id == $parent and (.result | fromjson? | .worker_result.kind) == "cdip.stage_terminal_decode")]
  | length >= 1
  and all((.result | fromjson | .worker_result.runner_mode) == "llama.cpp-stage-daemon")
  and all((.result | fromjson | .worker_result.stage_daemon_decode.next_token_id) != null)
' "$RUN_DIR/dispatch-jobs.json" >/dev/null

extract_stage_runner_json "$STAGE_WORK_DIR-dispatch/stage-0/stage-runner-source-decode.json" >"$RUN_DIR/dispatch-source-runner-final.json"
jq -e '.kv_session.enabled == true and .kv_session.loaded_bytes > 0 and .kv_session.saved_bytes > 0 and .kv_session.position_offset > 0' \
  "$RUN_DIR/dispatch-source-runner-final.json" >/dev/null
test -n "$(find "$STAGE_WORK_DIR-dispatch" -type f -path '*/sessions/*.seq' -size +0c -print -quit)"

jq -e --arg parent "$dispatch_parent_id" --arg kv "cdip-session-${dispatch_parent_id}:kv" '
  [.jobs[] | select(.type == "model.generate.distributed.stage" and .cdip_parent_job_id == $parent) | (.input | fromjson)]
  | length >= 2
  and all(.step == 3 and .kv_cache_key == $kv)
  and any(.stage_command == "source_decode" and .previous_token_id != null)
  and any(.stage_command == "terminal_decode")
' "$RUN_DIR/dispatch-jobs.json" >/dev/null

cat >"$RUN_DIR/summary.json" <<JSON
{
  "model": "$MODEL_PATH",
  "model_id": "$MODEL_ID",
  "n_layer": $N_LAYER,
  "parent_job": "$parent_id",
  "dispatch_parent_job": "$dispatch_parent_id",
  "source_daemon": "$DAEMON_A",
  "terminal_daemon": "$DAEMON_B",
  "next_token_id": $(jq -r '.result | fromjson | .next_token_id' "$RUN_DIR/parent.json"),
  "output": $(jq -r '.result | fromjson | .output | @json' "$RUN_DIR/parent.json"),
  "dispatch_next_token_id": $(jq -r '.result | fromjson | .next_token_id' "$RUN_DIR/dispatch-parent-final.json"),
  "dispatch_output": $(jq -r '.result | fromjson | .output | @json' "$RUN_DIR/dispatch-parent-final.json")
}
JSON

echo "PASS: CDIP real GGUF worker execution smoke succeeded"
echo "Evidence: $RUN_DIR"
