#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! MODEL_PATH="$("$ROOT_DIR/scripts/ensure-gguf-fixture.sh")"; then
  echo "SKIP: set CMESH_GGUF_MODEL_PATH=/path/to/model.gguf or CMESH_DOWNLOAD_GGUF_FIXTURE=1 to run physical stage GGUF worker smoke"
  exit 0
fi

RUN_DIR="${CMESH_CDIP_STAGE_GGUF_WORKER_SMOKE_DIR:-/tmp/cmesh-cdip-stage-gguf-worker-smoke-$(date -u +%Y%m%d%H%M%S)}"
PORT="${CMESH_CDIP_STAGE_GGUF_WORKER_SMOKE_PORT:-0}"
MODEL_ID="${CMESH_MODEL_ID:-qwen2.5-14b-instruct-q4-k-m}"
STAGE_WORK_DIR="${CMESH_STAGE_WORK_DIR:-$RUN_DIR/stage-work}"
TIMEOUT_MS="${CMESH_STAGE_TIMEOUT_MS:-60000}"
PROMPT="${CMESH_STAGE_PIPELINE_PROMPT:-hello from cmesh physical stage worker dispatch}"
LLAMA_WORK_DIR="${WORK_DIR:-/tmp/cmesh-llama-stage-runner}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "FAIL: $1 is required" >&2
    exit 1
  }
}

write_stage() {
  local index="$1"
  local start="$2"
  local end="$3"
  local first="$4"
  local terminal="$5"
  local bundle="$RUN_DIR/stage-$index.cmesh-shard"
  local gguf="$RUN_DIR/stage-$index.gguf"
  local args=(
    --command write-shard-bundle
    --model "$MODEL_PATH"
    --stage-start "$start"
    --stage-end "$end"
    --stage-index "$index"
    --output-file "$bundle"
  )
  if [[ "$first" == "true" ]]; then
    args+=(--first-stage)
  fi
  if [[ "$terminal" == "true" ]]; then
    args+=(--terminal-stage)
  fi

  "$RUNNER" "${args[@]}" >"$RUN_DIR/write-shard-bundle-$index.json"
  "$RUNNER" \
    --command verify-shard-bundle-source \
    --bundle-file "$bundle" \
    --model "$MODEL_PATH" \
    >"$RUN_DIR/verify-shard-bundle-source-$index.json"
  "$RUNNER" \
    --command write-stage-gguf-shard \
    --bundle-file "$bundle" \
    --model "$MODEL_PATH" \
    --output-file "$gguf" \
    >"$RUN_DIR/write-stage-gguf-shard-$index.json"
  "$RUNNER" \
    --command probe-stage-gguf-load \
    --model "$gguf" \
    >"$RUN_DIR/probe-stage-gguf-load-$index.json"
  jq -e --arg model "$gguf" '
    .kind == "cmesh.llamacpp_stage_gguf_load_probe" and
    .status == "stage_model_loaded_partial" and
    .model_path == $model and
    .loaded == true
  ' "$RUN_DIR/probe-stage-gguf-load-$index.json" >/dev/null
}

join_worker() {
  local name="$1"
  local response node_id
  response="$(curl -fsS -X POST "$MANAGER/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"${name}\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":8,\"cores_allowed\":4},\"memory\":{\"total_bytes\":10737418240,\"allowed_bytes\":8589934592},\"storage\":{\"total_bytes\":107374182400,\"allowed_bytes\":53687091200,\"free_bytes\":85899345920},\"models\":[{\"id\":\"${MODEL_ID}\",\"runtime\":\"llama.cpp\",\"path\":\"${MODEL_PATH}\",\"layers\":${N_LAYER},\"ready\":true}],\"runtimes\":[{\"name\":\"llama.cpp\",\"ready\":true,\"capabilities\":[\"logical-stage-runtime\",\"llama.cpp-stage-runner\",\"pipeline-stage-prepare\",\"pipeline-decode\",\"activation-stream-v1\"]}]},\"join_token\":\"smoke-token\"}")"
  node_id="$(jq -r '.node_id' <<<"$response")"
  jq -r '.node_auth_token' <<<"$response" >"$RUN_DIR/node-token-$node_id"
  printf '%s\n' "$node_id"
}

run_worker_once() {
  local node_id="$1"
  local force_final="${2:-}"
  local cache_dir="$RUN_DIR/cache-$node_id"
  local node_auth_token
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
      --cpu 4 \
      --memory-gb 8 \
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
    --cpu 4 \
    --memory-gb 8 \
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

need jq
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
if [[ -z "$N_LAYER" || "$N_LAYER" == "null" || "$N_LAYER" -lt 3 ]]; then
  echo "FAIL: expected model with at least 3 layers, got n_layer=$N_LAYER" >&2
  exit 1
fi

FIRST_END=$((N_LAYER / 3 - 1))
if [[ "$FIRST_END" -lt 0 ]]; then FIRST_END=0; fi
MIDDLE_START=$((FIRST_END + 1))
MIDDLE_END=$((2 * N_LAYER / 3 - 1))
if [[ "$MIDDLE_END" -lt "$MIDDLE_START" ]]; then MIDDLE_END="$MIDDLE_START"; fi
TERMINAL_START=$((MIDDLE_END + 1))
if [[ "$TERMINAL_START" -ge "$N_LAYER" ]]; then TERMINAL_START=$((N_LAYER - 1)); fi
TERMINAL_END=$((N_LAYER - 1))

write_stage 0 0 "$FIRST_END" true false
write_stage 1 "$MIDDLE_START" "$MIDDLE_END" false false
write_stage 2 "$TERMINAL_START" "$TERMINAL_END" false true

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

NODE_A="$(join_worker physical-stage-a)"
NODE_B="$(join_worker physical-stage-b)"
NODE_C="$(join_worker physical-stage-c)"

created="$(jq -n \
  --arg prompt "$PROMPT" \
  --arg runner "$RUNNER" \
  --arg stage0 "$RUN_DIR/stage-0.gguf" \
  --arg stage1 "$RUN_DIR/stage-1.gguf" \
  --arg stage2 "$RUN_DIR/stage-2.gguf" \
  --arg work_dir "$STAGE_WORK_DIR" \
  --argjson timeout_ms "$TIMEOUT_MS" \
  --argjson total_layers "$N_LAYER" \
  '{prompt:$prompt,max_tokens:1,temperature:"0.1",stage_runner_bin:$runner,stage_model_paths:[$stage0,$stage1,$stage2],work_dir:$work_dir,timeout_ms:$timeout_ms,total_layers:$total_layers}' \
  | curl -fsS -X POST "$MANAGER/v1/models/$MODEL_ID/distributed-generate" -H 'Content-Type: application/json' -d @-)"
echo "$created" >"$RUN_DIR/distributed-generate.json"
parent_id="$(jq -r '.job.id' "$RUN_DIR/distributed-generate.json")"
jq -e '.stage_jobs | length == 3' "$RUN_DIR/distributed-generate.json" >/dev/null
jq -c '.stage_jobs[] | {id, assigned_to, index:.cdip_stage_index, input:(.input | fromjson)}' "$RUN_DIR/distributed-generate.json" >"$RUN_DIR/stage-workers.jsonl"
jq -e --arg s0 "$RUN_DIR/stage-0.gguf" --arg s1 "$RUN_DIR/stage-1.gguf" --arg s2 "$RUN_DIR/stage-2.gguf" '
  [.stage_jobs[] | (.input | fromjson).model_path] == [$s0, $s1, $s2]
' "$RUN_DIR/distributed-generate.json" >/dev/null

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prepare" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/prepare.json"
while IFS= read -r stage_worker; do
  run_worker_once "$(jq -r '.assigned_to' <<<"$stage_worker")"
done <"$RUN_DIR/stage-workers.jsonl"

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prefill" -H 'Content-Type: application/json' -d '{}' >"$RUN_DIR/prefill.json"
curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/decode" -H 'Content-Type: application/json' -d '{"mode":"relay_decode","step":1}' >"$RUN_DIR/decode.json"

source_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].assigned_to' "$RUN_DIR/decode.json")"
relay_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[1].assigned_to' "$RUN_DIR/decode.json")"
terminal_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[2].assigned_to' "$RUN_DIR/decode.json")"
run_worker_once "$source_node"
run_worker_once "$relay_node"
run_worker_once "$terminal_node"

curl -fsS "$MANAGER/v1/jobs/$parent_id" >"$RUN_DIR/parent.json"
jq -e '.status == "succeeded" and (.result | fromjson | .kind == "cdip.distributed_terminal_result") and ((.result | fromjson | .tokens | length) >= 1)' "$RUN_DIR/parent.json" >/dev/null

extract_stage_runner_json "$STAGE_WORK_DIR/stage-0/stage-runner-source-decode.json" >"$RUN_DIR/source-runner.json"
extract_stage_runner_json "$STAGE_WORK_DIR/stage-1/stage-runner-decode.json" >"$RUN_DIR/relay-runner.json"
extract_stage_runner_json "$STAGE_WORK_DIR/stage-2/stage-runner-terminal-decode.json" >"$RUN_DIR/terminal-runner.json"
jq -e --arg model "$RUN_DIR/stage-0.gguf" '.model_path == $model and .status == "executed"' "$RUN_DIR/source-runner.json" >/dev/null
jq -e --arg model "$RUN_DIR/stage-1.gguf" '.model_path == $model and .status == "executed"' "$RUN_DIR/relay-runner.json" >/dev/null
jq -e --arg model "$RUN_DIR/stage-2.gguf" '.model_path == $model and .status == "executed"' "$RUN_DIR/terminal-runner.json" >/dev/null

cat >"$RUN_DIR/summary.json" <<JSON
{
  "status": "cdip_physical_stage_gguf_worker_execution_succeeded",
  "source_model": "$MODEL_PATH",
  "model_id": "$MODEL_ID",
  "parent_job": "$parent_id",
  "nodes": ["$NODE_A", "$NODE_B", "$NODE_C"],
  "stage_model_paths": [
    "$RUN_DIR/stage-0.gguf",
    "$RUN_DIR/stage-1.gguf",
    "$RUN_DIR/stage-2.gguf"
  ],
  "next_token_id": $(jq -r '.result | fromjson | .next_token_id' "$RUN_DIR/parent.json"),
  "output": $(jq -r '.result | fromjson | .output | @json' "$RUN_DIR/parent.json")
}
JSON

echo "PASS: CDIP physical stage GGUF worker execution smoke succeeded"
echo "Evidence: $RUN_DIR"
