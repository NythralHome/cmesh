#!/usr/bin/env bash
set -euo pipefail

MODEL_ID="${CMESH_MODEL_ID:-qwen2.5-7b-instruct-q4-k-m}"
PROMPT="${CMESH_PROMPT:-Run a local CDIP two-stage lifecycle smoke.}"
OUT_DIR="${CMESH_CDIP_SMOKE_DIR:-/tmp/cmesh-cdip-two-stage-smoke-$(date -u +%Y%m%d%H%M%S)}"
MANAGER_PORT="${CMESH_MANAGER_PORT:-}"
MANAGER_PID=""

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: $1 is required" >&2
    exit 1
  }
}

cleanup() {
  rc=$?
  if [ -n "$MANAGER_PID" ] && kill -0 "$MANAGER_PID" >/dev/null 2>&1; then
    kill "$MANAGER_PID" >/dev/null 2>&1 || true
    wait "$MANAGER_PID" >/dev/null 2>&1 || true
  fi
  exit "$rc"
}
trap cleanup EXIT

random_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

wait_manager() {
  i=0
  while [ "$i" -lt 120 ]; do
    if curl -fsS "$MANAGER_URL/health" >/dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
    sleep 0.25
  done
  echo "error: manager did not become healthy" >&2
  cat "$OUT_DIR/manager.log" >&2 || true
  exit 1
}

join_worker() {
  name="$1"
  curl -fsS -X POST -H "Content-Type: application/json" \
    -d "$(jq -n --arg name "$name" --arg model "$MODEL_ID" '{
      node_name: $name,
      role: "worker",
      resources: {
        cpu: {cores_total: 8, cores_allowed: 4},
        memory: {total_bytes: 17179869184, allowed_bytes: 12884901888},
        gpu: [],
        storage: {total_bytes: 137438953472, allowed_bytes: 107374182400, free_bytes: 85899345920},
        job_slots: 1,
        models: [{
          id: $model,
          name: $model,
          family: "qwen",
          runtime: "llama.cpp",
          path: ("/tmp/cmesh-smoke/" + $model + ".gguf"),
          bytes: 5000000000,
          ready: true
        }],
        runtimes: [{
          name: "llama.cpp",
          ready: true,
          version: "logical-stage-smoke",
          platform: "local/smoke",
          binary_path: "/usr/bin/false",
          source: "cdip-smoke",
          capabilities: [
            "pipeline-stage-prepare",
            "pipeline-prefill",
            "pipeline-decode",
            "activation-stream-v1",
            "logical-stage-runtime"
          ],
          stage_runtimes: [{
            name: "logical-stage",
            ready: true,
            cli_ready: true,
            required_hooks: ["mock activation relay"]
          }]
        }]
      }
    }')" \
    "$MANAGER_URL/v1/workers/join"
}

post_json() {
  path="$1"
  body="$2"
  curl -fsS -X POST -H "Content-Type: application/json" -d "$body" "$MANAGER_URL$path"
}

main() {
  need curl
  need jq
  need python3
  mkdir -p "$OUT_DIR"

  if [ -z "$MANAGER_PORT" ]; then
    MANAGER_PORT="$(random_port)"
  fi
  MANAGER_URL="http://127.0.0.1:$MANAGER_PORT"
  export MANAGER_URL

  echo "Starting temporary manager on $MANAGER_URL"
  go run ./cmd/cmesh manager start -addr "127.0.0.1:$MANAGER_PORT" -memory -cdip-auto-advance=false > "$OUT_DIR/manager.log" 2>&1 &
  MANAGER_PID="$!"
  echo "$MANAGER_PID" > "$OUT_DIR/manager.pid"
  wait_manager
  curl -fsS "$MANAGER_URL/health" | tee "$OUT_DIR/health.json" >/dev/null

  join_worker "cdip-stage-a" | tee "$OUT_DIR/join-a.json" >/dev/null
  join_worker "cdip-stage-b" | tee "$OUT_DIR/join-b.json" >/dev/null
  curl -fsS "$MANAGER_URL/v1/nodes" | tee "$OUT_DIR/nodes.json" >/dev/null

  curl -fsS "$MANAGER_URL/v1/models/$MODEL_ID/distributed-plan" | tee "$OUT_DIR/plan.json" >/dev/null
  jq '{feasible:.plan.feasible, executable_now:.plan.executable_now, stages:[.plan.stages[] | {index,node_id,node_name,layer_start,layer_end,stage_runtime,stage_runtime_ready,installed}], blockers:.plan.blockers, warnings:.plan.warnings}' "$OUT_DIR/plan.json"
  if [ "$(jq -r '.plan.feasible' "$OUT_DIR/plan.json")" != "true" ]; then
    echo "error: distributed plan is not feasible" >&2
    exit 1
  fi
  if [ "$(jq '.plan.stages | length' "$OUT_DIR/plan.json")" -lt 2 ]; then
    echo "error: expected at least two CDIP stages" >&2
    exit 1
  fi

  post_json "/v1/models/$MODEL_ID/distributed-generate" "$(jq -n --arg prompt "$PROMPT" '{prompt:$prompt,max_tokens:32,temperature:"0.1"}')" | tee "$OUT_DIR/distributed-generate.json" >/dev/null
  parent_id="$(jq -r '.job.id' "$OUT_DIR/distributed-generate.json")"
  jq -c '.stage_jobs[]' "$OUT_DIR/distributed-generate.json" > "$OUT_DIR/stage-jobs.jsonl"
  post_json "/v1/cdip/jobs/$parent_id/prepare" '{}' | tee "$OUT_DIR/prepare.json" >/dev/null

  echo "Preparing stages with local stage-runner..."
  while IFS= read -r stage_job; do
    stage_id="$(printf '%s' "$stage_job" | jq -r .id)"
    assigned_to="$(printf '%s' "$stage_job" | jq -r .assigned_to)"
    curl -fsS "$MANAGER_URL/v1/workers/$assigned_to/jobs/next" | tee "$OUT_DIR/$stage_id.next.json" >/dev/null
    printf '%s' "$stage_job" | jq -r .input > "$OUT_DIR/$stage_id.input.json"
    go run ./cmd/cmesh stage-runner prepare --input "$OUT_DIR/$stage_id.input.json" --mode logical | tee "$OUT_DIR/$stage_id.prepare.json" >/dev/null
    result="$(jq -c . "$OUT_DIR/$stage_id.prepare.json")"
    post_json "/v1/jobs/$stage_id/complete" "$(jq -n --arg node "$assigned_to" --arg result "$result" '{node_id:$node,result:$result}')" | tee "$OUT_DIR/$stage_id.complete.json" >/dev/null
  done < "$OUT_DIR/stage-jobs.jsonl"

  echo "Running CDIP lifecycle boundaries..."
  post_json "/v1/cdip/jobs/$parent_id/prefill" '{}' | tee "$OUT_DIR/prefill.json" >/dev/null

  stage0_id="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].id' "$OUT_DIR/distributed-generate.json")"
  downstream="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[1].assigned_to' "$OUT_DIR/distributed-generate.json")"
  go run ./cmd/cmesh stage-runner decode \
    --parent-job "$parent_id" \
    --stage-job "$stage0_id" \
    --stage-index 0 \
    --step 1 \
    --payload "activation-smoke" \
    --checksum "smoke:1" \
    --downstream-node "$downstream" \
    --manager "$MANAGER_URL" \
    --node-id "$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].assigned_to' "$OUT_DIR/distributed-generate.json")" \
    | tee "$OUT_DIR/stage-runner-decode.json" >/dev/null
  curl -fsS "$MANAGER_URL/v1/cdip/activations/$parent_id/$stage0_id/frames?timeout_ms=1000&node_id=$downstream" | tee "$OUT_DIR/activation-frame.json" >/dev/null
  jq '{sequence:.header.sequence, bytes:.header.payload_bytes, checksum:.header.checksum, payload:(.payload | @base64)}' "$OUT_DIR/activation-frame.json"

  post_json "/v1/cdip/jobs/$parent_id/decode" '{"step":1}' | tee "$OUT_DIR/decode.json" >/dev/null
  post_json "/v1/cdip/jobs/$parent_id/complete" '{"output":"cdip two-stage smoke completed"}' | tee "$OUT_DIR/complete.json" >/dev/null

  if [ "$(jq -r '.parent_job.status' "$OUT_DIR/complete.json")" != "succeeded" ]; then
    echo "error: parent job did not succeed" >&2
    jq . "$OUT_DIR/complete.json" >&2
    exit 1
  fi

  jq '{parent_job:.parent_job.id,status:.parent_job.status,stage_count:(.stage_jobs|length),stage_states:[.stage_jobs[] | {id,cdip_state,status}]}' "$OUT_DIR/complete.json" | tee "$OUT_DIR/summary.json"
  echo "PASS: CDIP two-stage smoke succeeded"
  echo "Evidence: $OUT_DIR"
}

main "$@"
