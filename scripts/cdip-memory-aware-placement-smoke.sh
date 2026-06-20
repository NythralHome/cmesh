#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_CDIP_MEMORY_PLACEMENT_SMOKE_DIR:-/tmp/cmesh-cdip-memory-placement-smoke-$(date -u +%Y%m%d%H%M%S)}"
PORT="${CMESH_CDIP_MEMORY_PLACEMENT_SMOKE_PORT:-0}"
MODEL_ID="${CMESH_MODEL_ID:-qwen2.5-14b-instruct-q4-k-m}"
JOIN_TOKEN="${CMESH_JOIN_TOKEN:-smoke-token}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "FAIL: $1 is required" >&2
    exit 1
  }
}

pick_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

wait_manager() {
  for _ in $(seq 1 100); do
    if curl -fsS "$MANAGER/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  echo "FAIL: manager did not become healthy" >&2
  cat "$RUN_DIR/manager.log" >&2 || true
  exit 1
}

join_stage_worker() {
  local index="$1"
  local name="memory-stage-$index"
  local endpoint="http://10.200.0.$((index + 10)):19781"
  curl -fsS -X POST "$MANAGER/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{
      \"node_name\":\"$name\",
      \"role\":\"worker\",
      \"join_token\":\"$JOIN_TOKEN\",
      \"resources\":{
        \"cpu\":{\"cores_total\":8,\"cores_allowed\":4},
        \"memory\":{\"total_bytes\":10737418240,\"allowed_bytes\":8589934592},
        \"storage\":{\"total_bytes\":137438953472,\"allowed_bytes\":12884901888,\"free_bytes\":12884901888},
        \"models\":[{
          \"id\":\"$MODEL_ID\",
          \"runtime\":\"llama.cpp\",
          \"path\":\"/var/lib/cmesh/stage-shards/stage-$index.gguf\",
          \"layers\":48,
          \"ready\":true
        }],
        \"runtimes\":[{
          \"name\":\"llama.cpp\",
          \"ready\":true,
          \"version\":\"memory-placement-smoke\",
          \"capabilities\":[
            \"pipeline-stage-prepare\",
            \"pipeline-prefill\",
            \"pipeline-decode\",
            \"activation-stream-v1\",
            \"logical-stage-runtime\"
          ],
          \"stage_runtimes\":[{
            \"name\":\"cmesh-stage-daemon\",
            \"ready\":true,
            \"endpoint\":\"$endpoint\",
            \"protocol\":\"cdip.stage-session-v1\"
          }]
        }]
      }
    }" >"$RUN_DIR/join-$index.json"
}

main() {
  need curl
  need jq
  need python3
  mkdir -p "$RUN_DIR"

  if [[ "$PORT" == "0" ]]; then
    PORT="$(pick_port)"
  fi
  MANAGER="http://127.0.0.1:${PORT}"
  export MANAGER

  BIN="$RUN_DIR/cmesh"
  go build -o "$BIN" "$ROOT_DIR/cmd/cmesh"

  "$BIN" manager start --memory --addr "127.0.0.1:${PORT}" --join-token "$JOIN_TOKEN" --cdip-auto-advance=false >"$RUN_DIR/manager.log" 2>&1 &
  MANAGER_PID=$!
  trap 'kill "$MANAGER_PID" >/dev/null 2>&1 || true' EXIT
  wait_manager

  for index in 0 1 2; do
    join_stage_worker "$index"
  done
  curl -fsS "$MANAGER/v1/nodes" >"$RUN_DIR/nodes.json"
  curl -fsS "$MANAGER/v1/models/$MODEL_ID/distributed-plan" >"$RUN_DIR/distributed-plan.json"

  jq -e '
    .plan.feasible == true
    and .plan.executable_now == true
    and (.plan.stages | length) == 3
    and .plan.aggregate_memory_bytes == 25769803776
    and .plan.aggregate_stage_memory_bytes == 24159191040
    and .plan.stage_runtime_diagnostics.resident_stage_workers == 3
    and (.plan.blockers | length == 0)
    and all(.plan.stages[]; .memory_bytes <= .allowed_memory_bytes and .stage_daemon_url != "" and .installed == true)
  ' "$RUN_DIR/distributed-plan.json" >/dev/null

  jq -c '.plan.placement.candidates[] | {node_name, selected, assigned_layers, assigned_memory_bytes, allowed_memory_bytes, remaining_memory_bytes}' \
    "$RUN_DIR/distributed-plan.json" >"$RUN_DIR/placement-candidates.jsonl"

  if grep -F "distributed tensor runtime adapter" "$RUN_DIR/distributed-plan.json" >/dev/null; then
    echo "FAIL: stale tensor runtime adapter blocker is still present" >&2
    jq . "$RUN_DIR/distributed-plan.json" >&2
    exit 1
  fi

  echo "PASS: memory-aware distributed placement is executable for $MODEL_ID"
  echo "evidence: $RUN_DIR"
}

main "$@"
