#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_ID="${CMESH_RUN_ID:-cmesh-qwen-placement-preflight-$(date -u +%Y%m%d%H%M%S)}"
OUT_DIR="${CMESH_EVIDENCE_DIR:-/tmp/$RUN_ID}"
PORT="${CMESH_MANAGER_PORT:-19180}"
MANAGER="http://127.0.0.1:$PORT"
JOIN_TOKEN="${CMESH_JOIN_TOKEN:-preflight-token}"
WORKER_MEMORY_GB="${CMESH_STAGE_WORKER_MEMORY_GB:-8}"
WORKER_DISK_GB="${CMESH_STAGE_WORKER_DISK_GB:-50}"
MODELS="${CMESH_QWEN_PREFLIGHT_MODELS:-qwen2.5-coder-32b-instruct-q4-k-m deepseek-r1-distill-qwen-32b-q4-k-m}"

mkdir -p "$OUT_DIR"

mem_bytes=$((WORKER_MEMORY_GB * 1024 * 1024 * 1024))
disk_bytes=$((WORKER_DISK_GB * 1024 * 1024 * 1024))
bin="$OUT_DIR/cmesh"

cleanup() {
  if [[ -n "${manager_pid:-}" ]]; then
    kill "$manager_pid" >/dev/null 2>&1 || true
    wait "$manager_pid" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

(cd "$ROOT_DIR" && go build -o "$bin" ./cmd/cmesh)
"$bin" manager start --memory --addr "127.0.0.1:$PORT" --join-token "$JOIN_TOKEN" --cdip-auto-advance=false >"$OUT_DIR/manager.log" 2>&1 &
manager_pid=$!

for _ in $(seq 1 100); do
  if curl -fsS "$MANAGER/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
curl -fsS "$MANAGER/health" >"$OUT_DIR/health.json"

join_worker() {
  local name="$1"
  curl -fsS -X POST "$MANAGER/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{
      \"node_name\":\"$name\",
      \"role\":\"worker\",
      \"resources\":{
        \"cpu\":{\"cores_total\":4,\"cores_allowed\":2},
        \"memory\":{\"total_bytes\":$mem_bytes,\"allowed_bytes\":$mem_bytes},
        \"storage\":{\"total_bytes\":$disk_bytes,\"allowed_bytes\":$disk_bytes,\"free_bytes\":$disk_bytes},
        \"runtimes\":[{
          \"name\":\"llama.cpp\",
          \"ready\":true,
          \"capabilities\":[\"llama.cpp-stage-runtime\",\"llama.cpp-stage-runner\"],
          \"stage_runtimes\":[{
            \"name\":\"cmesh-stage-daemon\",
            \"ready\":true,
            \"endpoint\":\"http://127.0.0.1:19781\",
            \"protocol\":\"cdip.stage-session-v1\"
          }]
        }]
      },
      \"join_token\":\"$JOIN_TOKEN\"
    }"
}

join_worker "preflight-stage-0" >"$OUT_DIR/join-stage-0.json"
join_worker "preflight-stage-1" >"$OUT_DIR/join-stage-1.json"
join_worker "preflight-stage-2" >"$OUT_DIR/join-stage-2.json"
curl -fsS "$MANAGER/v1/nodes" >"$OUT_DIR/nodes.json"

summary="$OUT_DIR/summary.jsonl"
: >"$summary"
for model_id in $MODELS; do
  model_dir="$OUT_DIR/$model_id"
  mkdir -p "$model_dir"
  curl -fsS "$MANAGER/v1/models/$model_id/distributed-plan" >"$model_dir/plan.json"
  jq -c --arg model_id "$model_id" --arg evidence "$model_dir" '{
    model: $model_id,
    evidence: $evidence,
    feasible: .plan.feasible,
    executable_now: .plan.executable_now,
    required_memory_bytes: .plan.required_memory_bytes,
    aggregate_stage_memory_bytes: .plan.aggregate_stage_memory_bytes,
    blockers: .plan.blockers,
    candidate_count: (.plan.placement.candidates | length),
    selected_candidates: (.plan.placement.candidates | map(select(.selected == true)) | length)
  }' "$model_dir/plan.json" >>"$summary"
done

echo "evidence: $OUT_DIR"
