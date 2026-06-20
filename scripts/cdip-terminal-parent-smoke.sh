#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_CDIP_TERMINAL_SMOKE_DIR:-/tmp/cmesh-cdip-terminal-parent-smoke-$(date -u +%Y%m%d%H%M%S)}"
PORT="${CMESH_CDIP_TERMINAL_SMOKE_PORT:-0}"

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
  local node_id
  node_id="$(curl -fsS -X POST "$MANAGER/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"${name}\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":4,\"cores_allowed\":2},\"memory\":{\"total_bytes\":8589934592,\"allowed_bytes\":6442450944},\"storage\":{\"total_bytes\":21474836480,\"allowed_bytes\":10737418240,\"free_bytes\":17179869184},\"models\":[{\"id\":\"gemma-3-12b-it-q4-k-m\",\"runtime\":\"llama.cpp\",\"path\":\"/tmp/model.gguf\",\"ready\":true}],\"runtimes\":[{\"name\":\"llama.cpp\",\"ready\":true,\"capabilities\":[\"logical-stage-runtime\"]}]},\"join_token\":\"smoke-token\"}" \
    | jq -r '.node_id')"
  printf '%s' "$node_id"
}

NODE_A="$(join_worker terminal-stage-a)"
NODE_B="$(join_worker terminal-stage-b)"
NODE_C="$(join_worker terminal-stage-c)"

create_body="$(cat <<JSON
{
  "model_id": "gemma-3-12b-it-q4-k-m",
  "prompt": "hello",
  "stages": [
    {"index":0,"node_id":"${NODE_A}","layer_start":0,"layer_end":9,"layers":10},
    {"index":1,"node_id":"${NODE_B}","layer_start":10,"layer_end":19,"layers":10},
    {"index":2,"node_id":"${NODE_C}","layer_start":20,"layer_end":31,"layers":12}
  ]
}
JSON
)"

parent="$(curl -fsS -X POST "$MANAGER/v1/models/gemma-3-12b-it-q4-k-m/distributed-generate" -H 'Content-Type: application/json' -d "$create_body")"
parent_id="$(jq -r '.job.id' <<<"$parent")"
jq -r '.stage_jobs[].id' <<<"$parent" >"$RUN_DIR/stage-ids.txt"
STAGE_IDS=()
while IFS= read -r stage_id; do
  STAGE_IDS+=("$stage_id")
done <"$RUN_DIR/stage-ids.txt"

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prepare" -H 'Content-Type: application/json' -d '{}' >/dev/null
for i in 0 1 2; do
  node_var="NODE_$((i + 1))"
  node_id="${!node_var:-}"
  if [[ "$i" == "0" ]]; then node_id="$NODE_A"; fi
  if [[ "$i" == "1" ]]; then node_id="$NODE_B"; fi
  if [[ "$i" == "2" ]]; then node_id="$NODE_C"; fi
  next="$(curl -fsS "$MANAGER/v1/workers/${node_id}/jobs/next")"
  job_id="$(jq -r '.job.id' <<<"$next")"
  curl -fsS -X POST "$MANAGER/v1/jobs/${job_id}/complete" \
    -H 'Content-Type: application/json' \
    -d "{\"node_id\":\"${node_id}\",\"result\":\"{\\\"kind\\\":\\\"cdip.stage_ready\\\"}\"}" >/dev/null
done

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prefill" -H 'Content-Type: application/json' -d '{}' >/dev/null
curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/decode" -H 'Content-Type: application/json' -d '{"mode":"relay_decode","step":1}' >/dev/null

terminal_next="$(curl -fsS "$MANAGER/v1/workers/${NODE_C}/jobs/next")"
terminal_job_id="$(jq -r '.job.id' <<<"$terminal_next")"
terminal_result="$(cat <<JSON
{
  "kind": "cdip.stage_terminal_decode",
  "parent_job_id": "${parent_id}",
  "upstream_stage_job_id": "${STAGE_IDS[1]}",
  "stage_job_id": "${terminal_job_id}",
  "stage_index": 2,
  "next_token_id": 42,
  "next_token_text": " hello",
  "tokens": [42, 43, 44],
  "output": " hello world",
  "final": true
}
JSON
)"
complete_body="$(jq -nc --arg node "$NODE_C" --arg result "$terminal_result" '{node_id:$node,result:$result}')"
completed="$(curl -fsS -X POST "$MANAGER/v1/jobs/${terminal_job_id}/complete" -H 'Content-Type: application/json' -d "$complete_body")"
echo "$completed" >"$RUN_DIR/completed-parent.json"

jq -e '.id == "'"${parent_id}"'" and .status == "succeeded" and (.result | fromjson | .kind == "cdip.distributed_terminal_result") and (.result | fromjson | .output == " hello world") and (.result | fromjson | .token_count == 3) and (.result | fromjson | .final == true)' "$RUN_DIR/completed-parent.json" >/dev/null

echo "PASS: CDIP terminal parent smoke succeeded"
echo "Evidence: $RUN_DIR"
