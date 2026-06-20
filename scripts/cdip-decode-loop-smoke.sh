#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_CDIP_DECODE_LOOP_SMOKE_DIR:-/tmp/cmesh-cdip-decode-loop-smoke-$(date -u +%Y%m%d%H%M%S)}"
PORT="${CMESH_CDIP_DECODE_LOOP_SMOKE_PORT:-0}"

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
    -d "{\"node_name\":\"${name}\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":4,\"cores_allowed\":2},\"memory\":{\"total_bytes\":8589934592,\"allowed_bytes\":6442450944},\"storage\":{\"total_bytes\":21474836480,\"allowed_bytes\":10737418240,\"free_bytes\":17179869184},\"models\":[{\"id\":\"gemma-3-12b-it-q4-k-m\",\"runtime\":\"llama.cpp\",\"path\":\"/tmp/model.gguf\",\"ready\":true}],\"runtimes\":[{\"name\":\"llama.cpp\",\"ready\":true,\"capabilities\":[\"logical-stage-runtime\"]}]},\"join_token\":\"smoke-token\"}" \
    | jq -r '.node_id'
}

NODE_A="$(join_worker decode-loop-a)"
NODE_B="$(join_worker decode-loop-b)"
NODE_C="$(join_worker decode-loop-c)"

create_body="$(cat <<JSON
{
  "prompt": "hello",
  "max_tokens": 8,
  "stages": [
    {"index":0,"node_id":"${NODE_A}","layer_start":0,"layer_end":15,"layers":16},
    {"index":1,"node_id":"${NODE_B}","layer_start":16,"layer_end":31,"layers":16},
    {"index":2,"node_id":"${NODE_C}","layer_start":32,"layer_end":47,"layers":16}
  ]
}
JSON
)"

created="$(curl -fsS -X POST "$MANAGER/v1/models/gemma-3-12b-it-q4-k-m/distributed-generate" -H 'Content-Type: application/json' -d "$create_body")"
parent_id="$(jq -r '.job.id' <<<"$created")"

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prepare" -H 'Content-Type: application/json' -d '{}' >/dev/null
for node_id in "$NODE_A" "$NODE_B" "$NODE_C"; do
  next="$(curl -fsS "$MANAGER/v1/workers/${node_id}/jobs/next")"
  job_id="$(jq -r '.job.id' <<<"$next")"
  curl -fsS -X POST "$MANAGER/v1/jobs/${job_id}/complete" \
    -H 'Content-Type: application/json' \
    -d "{\"node_id\":\"${node_id}\",\"result\":\"{\\\"kind\\\":\\\"cdip.stage_ready\\\"}\"}" >/dev/null
done

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/prefill" -H 'Content-Type: application/json' -d '{}' >/dev/null
loop_result="$(curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${parent_id}/decode-loop" -H 'Content-Type: application/json' -d '{"max_tokens":3}')"
echo "$loop_result" >"$RUN_DIR/decode-loop-result.json"

jq -e '
  . as $root |
  .parent_job.id == "'"${parent_id}"'" and
  .parent_job.status == "succeeded" and
  .final == true and
  .output == " token-1 token-2 token-3" and
  (.chunks | length == 3) and
  (.messages | length == 9) and
  .trace.protocol == "cdip" and
  .trace.session_id == ("cdip-session-" + "'"${parent_id}"'") and
  .trace.kv_cache_key == (.trace.session_id + ":kv") and
  .trace.stage_count == 3 and
  (.chunks | all(.kv_cache_key == $root.trace.kv_cache_key)) and
  (.parent_job.result | fromjson | .token_count == 3) and
  (.parent_job.result | fromjson | .trace.kv_cache_key == $root.trace.kv_cache_key)
' "$RUN_DIR/decode-loop-result.json" >/dev/null

dispatch_created="$(curl -fsS -X POST "$MANAGER/v1/models/gemma-3-12b-it-q4-k-m/distributed-generate" -H 'Content-Type: application/json' -d "$create_body")"
echo "$dispatch_created" >"$RUN_DIR/dispatch-created.json"
dispatch_parent_id="$(jq -r '.job.id' <<<"$dispatch_created")"

curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${dispatch_parent_id}/prepare" -H 'Content-Type: application/json' -d '{}' >/dev/null
for node_id in "$NODE_A" "$NODE_B" "$NODE_C"; do
  next="$(curl -fsS "$MANAGER/v1/workers/${node_id}/jobs/next")"
  job_id="$(jq -r '.job.id' <<<"$next")"
  curl -fsS -X POST "$MANAGER/v1/jobs/${job_id}/complete" \
    -H 'Content-Type: application/json' \
    -d "{\"node_id\":\"${node_id}\",\"result\":\"{\\\"kind\\\":\\\"cdip.stage_ready\\\"}\"}" >/dev/null
done
curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${dispatch_parent_id}/prefill" -H 'Content-Type: application/json' -d '{}' >/dev/null
dispatch_result="$(curl -fsS -X POST "$MANAGER/v1/cdip/jobs/${dispatch_parent_id}/decode-loop" -H 'Content-Type: application/json' -d '{"mode":"dispatch","step":2,"max_tokens":5}')"
echo "$dispatch_result" >"$RUN_DIR/decode-loop-dispatch-result.json"

jq -e '
  .parent_job.id == "'"${dispatch_parent_id}"'" and
  .parent_job.status != "succeeded" and
  (.stage_jobs | length == 3) and
  (.messages | length == 3) and
  (.messages | all(.step == 2)) and
  .trace.mode == "worker-dispatch" and
  .trace.session_id == ("cdip-session-" + "'"${dispatch_parent_id}"'") and
  .trace.kv_cache_key == (.trace.session_id + ":kv") and
  .trace.max_tokens == 5 and
  (.stage_jobs | all(.status == "scheduled" and .cdip_state == "decode"))
' "$RUN_DIR/decode-loop-dispatch-result.json" >/dev/null

for node_id in "$NODE_A" "$NODE_B" "$NODE_C"; do
  next="$(curl -fsS "$MANAGER/v1/workers/${node_id}/jobs/next")"
  echo "$next" >"$RUN_DIR/dispatch-next-${node_id}.json"
done

jq -e '.job.input | fromjson | .stage_command == "source_decode" and .step == 2 and .kv_cache_key == ("cdip-session-" + "'"${dispatch_parent_id}"'" + ":kv")' "$RUN_DIR/dispatch-next-${NODE_A}.json" >/dev/null
jq -e '.job.input | fromjson | .stage_command == "relay_decode" and .step == 2 and .kv_cache_key == ("cdip-session-" + "'"${dispatch_parent_id}"'" + ":kv")' "$RUN_DIR/dispatch-next-${NODE_B}.json" >/dev/null
jq -e '.job.input | fromjson | .stage_command == "terminal_decode" and .step == 2 and .kv_cache_key == ("cdip-session-" + "'"${dispatch_parent_id}"'" + ":kv")' "$RUN_DIR/dispatch-next-${NODE_C}.json" >/dev/null

source_job_id="$(jq -r '.job.id' "$RUN_DIR/dispatch-next-${NODE_A}.json")"
relay_job_id="$(jq -r '.job.id' "$RUN_DIR/dispatch-next-${NODE_B}.json")"
terminal_job_id="$(jq -r '.job.id' "$RUN_DIR/dispatch-next-${NODE_C}.json")"
curl -fsS -X POST "$MANAGER/v1/jobs/${source_job_id}/complete" \
  -H 'Content-Type: application/json' \
  -d "{\"node_id\":\"${NODE_A}\",\"result\":\"{\\\"kind\\\":\\\"cdip.stage_source_decode\\\"}\"}" >/dev/null
curl -fsS -X POST "$MANAGER/v1/jobs/${relay_job_id}/complete" \
  -H 'Content-Type: application/json' \
  -d "{\"node_id\":\"${NODE_B}\",\"result\":\"{\\\"kind\\\":\\\"cdip.stage_relay_decode\\\"}\"}" >/dev/null
partial_terminal_result="$(jq -n \
  --arg parent "$dispatch_parent_id" \
  --arg upstream "$relay_job_id" \
  --arg terminal "$terminal_job_id" \
  --arg kv "cdip-session-${dispatch_parent_id}:kv" \
  '{kind:"cdip.stage_terminal_decode",parent_job_id:$parent,upstream_stage_job_id:$upstream,stage_job_id:$terminal,stage_index:2,step:2,kv_cache_key:$kv,next_token_id:202,next_token_text:" token-2",tokens:[101,202],output:" token-1 token-2",final:false}' \
  | jq -Rs .)"
curl -fsS -X POST "$MANAGER/v1/jobs/${terminal_job_id}/complete" \
  -H 'Content-Type: application/json' \
  -d "{\"node_id\":\"${NODE_C}\",\"result\":${partial_terminal_result}}" >"$RUN_DIR/dispatch-partial-terminal-complete.json"
curl -fsS "$MANAGER/v1/jobs" >"$RUN_DIR/dispatch-after-partial-jobs.json"
jq -e --arg parent "$dispatch_parent_id" --arg kv "cdip-session-${dispatch_parent_id}:kv" '
  [.jobs[] | select(.type == "model.generate.distributed.stage" and .cdip_parent_job_id == $parent) | (.input | fromjson)]
  | length == 3
  and all(.step == 3 and .kv_cache_key == $kv)
  and any(.stage_command == "source_decode")
  and any(.stage_command == "relay_decode")
  and any(.stage_command == "terminal_decode")
' "$RUN_DIR/dispatch-after-partial-jobs.json" >/dev/null
curl -fsS "$MANAGER/v1/jobs/${dispatch_parent_id}" >"$RUN_DIR/dispatch-after-partial-parent.json"
jq -e '.status != "succeeded"' "$RUN_DIR/dispatch-after-partial-parent.json" >/dev/null

echo "PASS: CDIP decode-loop smoke succeeded"
echo "Evidence: $RUN_DIR"
