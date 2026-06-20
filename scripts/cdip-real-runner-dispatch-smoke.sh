#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_CDIP_REAL_RUNNER_DISPATCH_SMOKE_DIR:-/tmp/cmesh-cdip-real-runner-dispatch-smoke-$(date -u +%Y%m%d%H%M%S)}"
PORT="${CMESH_CDIP_REAL_RUNNER_DISPATCH_SMOKE_PORT:-0}"
MODEL_ID="${CMESH_MODEL_ID:-gemma-3-12b-it-q4-k-m}"
STAGE_RUNNER_BIN="${CMESH_STAGE_RUNNER_BIN:-/tmp/cmesh-stage-runner}"
MODEL_PATH="${CMESH_GGUF_MODEL_PATH:-/tmp/cmesh-model.gguf}"
WORK_DIR="${CMESH_STAGE_WORK_DIR:-$RUN_DIR/stage-work}"
TIMEOUT_MS="${CMESH_STAGE_TIMEOUT_MS:-120000}"

mkdir -p "$RUN_DIR"

BIN="$RUN_DIR/cmesh"
go build -o "$BIN" "$ROOT_DIR/cmd/cmesh"

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
  curl -fsS -X POST "$MANAGER/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"${name}\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":8,\"cores_allowed\":4},\"memory\":{\"total_bytes\":34359738368,\"allowed_bytes\":21474836480},\"storage\":{\"total_bytes\":107374182400,\"allowed_bytes\":53687091200,\"free_bytes\":85899345920},\"models\":[{\"id\":\"${MODEL_ID}\",\"runtime\":\"llama.cpp\",\"path\":\"${MODEL_PATH}\",\"ready\":true}],\"runtimes\":[{\"name\":\"llama.cpp\",\"ready\":true,\"capabilities\":[\"logical-stage-runtime\",\"llama.cpp-stage-runner\"]}]},\"join_token\":\"smoke-token\"}" \
    | jq -r '.node_id'
}

NODE_A="$(join_worker real-runner-stage-a)"
NODE_B="$(join_worker real-runner-stage-b)"
NODE_C="$(join_worker real-runner-stage-c)"

created="$(jq -n \
  --arg prompt "dispatch real stage runner metadata" \
  --arg runner "$STAGE_RUNNER_BIN" \
  --arg model_path "$MODEL_PATH" \
  --arg work_dir "$WORK_DIR" \
  --argjson timeout_ms "$TIMEOUT_MS" \
  '{prompt:$prompt,max_tokens:8,temperature:"0.1",stage_runner_bin:$runner,model_path:$model_path,work_dir:$work_dir,timeout_ms:$timeout_ms}' \
  | curl -fsS -X POST "$MANAGER/v1/models/$MODEL_ID/distributed-generate" -H 'Content-Type: application/json' -d @-)"
echo "$created" >"$RUN_DIR/distributed-generate.json"
parent_id="$(jq -r '.job.id' "$RUN_DIR/distributed-generate.json")"
jq -c '.stage_jobs[] | {id, assigned_to}' "$RUN_DIR/distributed-generate.json" >"$RUN_DIR/stage-workers.jsonl"

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prepare" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/prepare.json"
while IFS= read -r stage_worker; do
  node_id="$(jq -r '.assigned_to' <<<"$stage_worker")"
  next="$(curl -fsS "$MANAGER/v1/workers/${node_id}/jobs/next")"
  job_id="$(jq -r '.job.id' <<<"$next")"
  if [[ -z "$job_id" || "$job_id" == "null" ]]; then
    echo "worker $node_id did not receive prepare job" >&2
    exit 1
  fi
  curl -fsS -X POST "$MANAGER/v1/jobs/${job_id}/complete" \
    -H 'Content-Type: application/json' \
    -d "{\"node_id\":\"${node_id}\",\"result\":\"{\\\"kind\\\":\\\"cdip.stage_ready\\\"}\"}" >/dev/null
done <"$RUN_DIR/stage-workers.jsonl"

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prefill" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/prefill.json"
curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/decode" -H 'Content-Type: application/json' -d '{"mode":"relay_decode","step":1}' >"$RUN_DIR/decode.json"
curl -fsS "$MANAGER/v1/jobs" >"$RUN_DIR/jobs.json"

jq -e \
  --arg parent "$parent_id" \
  --arg runner "$STAGE_RUNNER_BIN" \
  --arg model_path "$MODEL_PATH" \
  --arg work_dir "$WORK_DIR" \
  --argjson timeout_ms "$TIMEOUT_MS" '
  [.jobs[]
    | select(.type == "model.generate.distributed.stage")
    | select(.cdip_parent_job_id == $parent)
    | (.input | fromjson)
  ] as $stages
  | ($stages | length) >= 2
  and any($stages[]; .stage_command == "source_decode")
  and any($stages[]; .stage_command == "terminal_decode")
  and all($stages[]; (.stage_command == "source_decode" or .stage_command == "relay_decode" or .stage_command == "terminal_decode"))
  and all($stages[]; .stage_runner_bin == $runner and .model_path == $model_path and .timeout_ms == $timeout_ms)
  and all($stages[]; .work_dir == ($work_dir + "/stage-" + (.stage.index | tostring)))
  ' "$RUN_DIR/jobs.json" >/dev/null

echo "PASS: CDIP real runner dispatch smoke succeeded"
echo "Evidence: $RUN_DIR"
