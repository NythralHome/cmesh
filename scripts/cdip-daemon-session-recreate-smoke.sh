#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_CDIP_DAEMON_SESSION_RECREATE_SMOKE_DIR:-/tmp/cmesh-cdip-daemon-session-recreate-smoke-$(date -u +%Y%m%d%H%M%S)}"
MANAGER_PORT="${CMESH_CDIP_DAEMON_SESSION_RECREATE_MANAGER_PORT:-0}"
DAEMON_PORT="${CMESH_CDIP_DAEMON_SESSION_RECREATE_DAEMON_PORT:-0}"
MODEL_ID="${CMESH_MODEL_ID:-gemma-3-12b-it-q4-k-m}"
MODEL_PATH="$RUN_DIR/model.gguf"
WORK_DIR="$RUN_DIR/stage-work"
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

  local bin fake_runner manager daemon
  bin="$RUN_DIR/cmesh"
  fake_runner="$RUN_DIR/cmesh-stage-runner"
  manager="http://127.0.0.1:${MANAGER_PORT}"
  daemon="http://127.0.0.1:${DAEMON_PORT}"

  go build -o "$bin" "$ROOT_DIR/cmd/cmesh"
  touch "$MODEL_PATH"

  cat >"$fake_runner" <<'SH'
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
{"kind":"cmesh.llamacpp_stage_prepare","status":"metadata_ready","runtime":"llama.cpp","model_path":"$(arg_value --model "$@")","model_name":"fake-session-recreate","stage_index":${stage_index:-0},"stage_start":${stage_start:-0},"stage_end":${stage_end:-0},"tensor_manifest":{"source":"fake","manifest_only":true,"total_tensor_count":2,"selected_tensor_count":1,"stage_tensor_count":1,"boundary_tensor_count":0,"selected_bytes":16,"tensors":[{"name":"blk.0.fake.weight","type":"F32","bytes":16,"boundary":false}]},"executable":false,"guardrail":"fake runner for session recreate smoke"}
JSON
    ;;
  write-shard-bundle)
    output_file="$(arg_value --output-file "$@")"
    mkdir -p "$(dirname "$output_file")"
    python3 - "$output_file" <<'PY'
import sys
with open(sys.argv[1], "wb") as f:
    f.write(b"CMESH_FAKE_RECREATE_BUNDLE")
PY
    cat <<JSON
{"kind":"cmesh.llamacpp_stage_shard_bundle","status":"bundle_ready_not_loadable_gguf","runtime":"llama.cpp","stage_index":${stage_index:-0},"stage_start":${stage_start:-0},"stage_end":${stage_end:-0},"output_file":"$output_file","selected_bytes":16,"bundle_bytes":26,"loadable_gguf":false,"guardrail":"fake shard bundle for session recreate smoke"}
JSON
    ;;
  source-decode)
    output_file="$(arg_value --output-file "$@")"
    mkdir -p "$(dirname "$output_file")"
    python3 - "$output_file" <<'PY'
import struct, sys
with open(sys.argv[1], "wb") as f:
    f.write(struct.pack("<ffff", 1.0, 2.0, 3.0, 4.0))
PY
    cat <<JSON
{"kind":"cmesh.llamacpp_stage_source_decode","status":"executed","runtime":"llama.cpp","stage_index":${stage_index:-0},"stage_start":${stage_start:-0},"stage_end":${stage_end:-0},"input_tensor":{"dtype":"tokens","shape":[1,1],"bytes":4},"output_tensor":{"dtype":"f32","shape":[1,1,4],"bytes":16,"path":"$output_file"},"decode_status":0}
JSON
    ;;
  terminal-decode)
    cat <<JSON
{"kind":"cmesh.llamacpp_stage_terminal_decode","status":"executed","runtime":"llama.cpp","stage_index":${stage_index:-1},"stage_start":${stage_start:-1},"stage_end":${stage_end:-1},"input_tensor":{"dtype":"$(arg_value --dtype "$@")","shape":[1,1,4],"bytes":16},"logits":{"dtype":"f32","shape":[1,4],"bytes":16},"next_token_id":7001,"next_token_text":" recovered","tokens":[7001],"output":" recovered","final":true,"decode_status":0}
JSON
    ;;
  *)
    echo "unsupported fake command $command" >&2
    exit 2
    ;;
esac
SH
  chmod +x "$fake_runner"

  "$bin" stage-runner daemon --addr "127.0.0.1:${DAEMON_PORT}" --session-dir "$RUN_DIR/stage-sessions" >"$RUN_DIR/stage-daemon.log" 2>&1 &
  DAEMON_PID=$!
  "$bin" manager start --memory --addr "127.0.0.1:${MANAGER_PORT}" --join-token smoke-token --cdip-auto-advance=false >"$RUN_DIR/manager.log" 2>&1 &
  MANAGER_PID=$!
  trap 'kill "${MANAGER_PID:-}" "${DAEMON_PID:-}" >/dev/null 2>&1 || true' EXIT

  wait_for_http "$manager/health"
  wait_for_http "$daemon/health"

  join_worker() {
    local name="$1"
    local response node_id
    response="$(curl -fsS -X POST "$manager/v1/workers/join" \
      -H 'Content-Type: application/json' \
      -d "{\"node_name\":\"${name}\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":8,\"cores_allowed\":4},\"memory\":{\"total_bytes\":34359738368,\"allowed_bytes\":21474836480},\"storage\":{\"total_bytes\":107374182400,\"allowed_bytes\":53687091200,\"free_bytes\":85899345920},\"models\":[{\"id\":\"${MODEL_ID}\",\"runtime\":\"llama.cpp\",\"path\":\"${MODEL_PATH}\",\"ready\":true}],\"runtimes\":[{\"name\":\"llama.cpp\",\"ready\":true,\"capabilities\":[\"logical-stage-runtime\",\"llama.cpp-stage-runtime\",\"llama.cpp-stage-runner\"],\"stage_runtimes\":[{\"name\":\"cmesh-stage-daemon\",\"ready\":true,\"endpoint\":\"${daemon}\",\"protocol\":\"cdip.stage-session-v1\"}]}]},\"join_token\":\"smoke-token\"}")"
    node_id="$(jq -r '.node_id' <<<"$response")"
    jq -r '.node_auth_token' <<<"$response" >"$RUN_DIR/node-token-$node_id"
    printf '%s\n' "$node_id"
  }

  local node_a node_b
  node_a="$(join_worker recreate-stage-a)"
  node_b="$(join_worker recreate-stage-b)"

  run_worker_once() {
    local node_id="$1"
    local node_auth_token
    node_auth_token="$(cat "$RUN_DIR/node-token-$node_id")"
    "$bin" worker poll-once \
      --manager "$manager" \
      --node-id "$node_id" \
      --node-auth-token "$node_auth_token" \
      --cache-dir "$RUN_DIR/cache-$node_id" \
      --model-id "$MODEL_ID" \
      --model-path "$MODEL_PATH" \
      --runtime "llama.cpp" \
      --cpu 4 \
      --memory-gb 20 \
      --disk-gb 50 >"$RUN_DIR/poll-$node_id-$(date -u +%s%N).log" 2>&1
  }

  jq -n \
    --arg prompt "daemon session recreate smoke" \
    --arg runner "$fake_runner" \
    --arg model_path "$MODEL_PATH" \
    --arg work_dir "$WORK_DIR" \
    '{prompt:$prompt,max_tokens:1,temperature:"0.1",stage_runner_bin:$runner,model_path:$model_path,work_dir:$work_dir,timeout_ms:5000}' \
    | curl -fsS -X POST "$manager/v1/models/$MODEL_ID/distributed-generate" -H 'Content-Type: application/json' -d @- \
    >"$RUN_DIR/distributed-generate.json"

  local parent_id source_node terminal_node source_session terminal_session
  parent_id="$(jq -r '.job.id' "$RUN_DIR/distributed-generate.json")"
  source_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].assigned_to' "$RUN_DIR/distributed-generate.json")"
  terminal_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].assigned_to' "$RUN_DIR/distributed-generate.json")"
  source_session="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].input | fromjson | .stage_session_id' "$RUN_DIR/distributed-generate.json")"
  terminal_session="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].input | fromjson | .stage_session_id' "$RUN_DIR/distributed-generate.json")"

  curl -fsS -X POST "$manager/v1/cdip/jobs/${parent_id}/prepare" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/prepare.json"
  run_worker_once "$node_a"
  run_worker_once "$node_b"

  curl -fsS "$daemon/v1/sessions/${source_session}" >"$RUN_DIR/source-session-before-delete.json"
  curl -fsS "$daemon/v1/sessions/${terminal_session}" >"$RUN_DIR/terminal-session-before-delete.json"
  curl -fsS -X DELETE "$daemon/v1/sessions/${source_session}" >"$RUN_DIR/source-session-delete.json"
  curl -fsS -X DELETE "$daemon/v1/sessions/${terminal_session}" >"$RUN_DIR/terminal-session-delete.json"
  jq -e '.closed == true' "$RUN_DIR/source-session-delete.json" >/dev/null
  jq -e '.closed == true' "$RUN_DIR/terminal-session-delete.json" >/dev/null
  curl -fsS "$daemon/health" >"$RUN_DIR/daemon-health-after-delete.json"
  jq -e '.session_count == 0' "$RUN_DIR/daemon-health-after-delete.json" >/dev/null

  curl -fsS -X POST "$manager/v1/cdip/jobs/${parent_id}/prefill" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/prefill.json"
  curl -fsS -X POST "$manager/v1/cdip/jobs/${parent_id}/decode-loop" -H 'Content-Type: application/json' -d '{"mode":"dispatch","step":1,"max_tokens":1}' >"$RUN_DIR/decode-loop.json"
  run_worker_once "$source_node"
  run_worker_once "$terminal_node"

  curl -fsS "$daemon/v1/sessions/${source_session}" >"$RUN_DIR/source-session-after-recreate.json"
  curl -fsS "$daemon/v1/sessions/${terminal_session}" >"$RUN_DIR/terminal-session-after-recreate.json"
  jq -e '.decode_steps == 1 and .last_stage_command == "source_decode" and .session.persistent_kv_in_memory == true' "$RUN_DIR/source-session-after-recreate.json" >/dev/null
  jq -e '.decode_steps == 1 and .last_stage_command == "terminal_decode" and .session.persistent_kv_in_memory == true' "$RUN_DIR/terminal-session-after-recreate.json" >/dev/null

  curl -fsS "$manager/v1/jobs/$parent_id" >"$RUN_DIR/parent.json"
  jq -e '.status == "succeeded" and (.result | fromjson | .output == " recovered")' "$RUN_DIR/parent.json" >/dev/null

  jq -n \
    --arg parent_id "$parent_id" \
    --arg source_session "$source_session" \
    --arg terminal_session "$terminal_session" \
    --arg evidence "$RUN_DIR" \
    '{status:"passed",parent_job_id:$parent_id,recreated_sessions:[$source_session,$terminal_session],evidence:$evidence}' \
    >"$RUN_DIR/summary.json"

  echo "PASS: CDIP daemon session recreate smoke succeeded"
  echo "Evidence: $RUN_DIR"
}

main "$@"
