#!/usr/bin/env bash
set -euo pipefail

MANAGER_URL="${CMESH_MANAGER_URL:-http://127.0.0.1:18080}"
OPERATOR_TOKEN="${CMESH_OPERATOR_TOKEN:-}"
MODEL_ID="${CMESH_MODEL_ID:-qwen2.5-0.5b-instruct-q4-k-m}"
PROMPT="${CMESH_PROMPT:-Reply in one short sentence: benchmark the current CMesh inference path.}"
MAX_TOKENS="${CMESH_MAX_TOKENS:-64}"
TEMPERATURE="${CMESH_TEMPERATURE:-0.2}"
OUT_DIR="${CMESH_BENCHMARK_DIR:-/tmp/cmesh-distributed-rpc-benchmark-$(date -u +%Y%m%d%H%M%S)}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: $1 is required" >&2
    exit 1
  }
}

curl_auth_args=()
if [ -n "$OPERATOR_TOKEN" ]; then
  curl_auth_args=(-H "Authorization: Bearer $OPERATOR_TOKEN")
fi

wait_job() {
  job_id="$1"
  limit="${2:-180}"
  i=1
  while [ "$i" -le "$limit" ]; do
    body="$(curl -fsS "${curl_auth_args[@]}" "$MANAGER_URL/v1/jobs/$job_id")"
    status="$(printf '%s' "$body" | jq -r .status)"
    printf '%s' "$body" > "$OUT_DIR/$job_id.json"
    echo "$job_id $status"
    case "$status" in
      succeeded)
        return 0
        ;;
      failed|canceled)
        printf '%s\n' "$body" | jq '{id,type,status,error,last_failure,result}'
        return 1
        ;;
    esac
    i=$((i + 1))
    sleep 2
  done
  echo "error: timed out waiting for $job_id" >&2
  return 1
}

submit_job() {
  mode="$1"
  node_id="$2"
  endpoints_json="$3"
  plan_json="$4"
  job_type="model.generate"
  input_json="$(jq -n \
    --arg model "$MODEL_ID" \
    --arg prompt "$PROMPT" \
    --arg temp "$TEMPERATURE" \
    --argjson max "$MAX_TOKENS" \
    '{model_id:$model,prompt:$prompt,max_tokens:$max,temperature:$temp}')"
  if [ "$mode" != "local" ]; then
    job_type="model.generate.distributed_rpc"
    input_json="$(jq -n \
      --arg model "$MODEL_ID" \
      --arg prompt "$PROMPT" \
      --arg temp "$TEMPERATURE" \
      --argjson max "$MAX_TOKENS" \
      --argjson endpoints "$endpoints_json" \
      --argjson plan "$plan_json" \
      '{model_id:$model,prompt:$prompt,max_tokens:$max,temperature:$temp,rpc_endpoints:$endpoints,execution_plan:$plan}')"
  fi
  request="$(jq -n \
    --arg type "$job_type" \
    --arg assigned "$node_id" \
    --arg requested "distributed-rpc-benchmark:$mode" \
    --arg input "$input_json" \
    '{type:$type,input:$input,assigned_to:$assigned,requested_by:$requested,max_attempts:1,requirements:{cpu_cores:1}}')"
  curl -fsS "${curl_auth_args[@]}" -H "Content-Type: application/json" -d "$request" "$MANAGER_URL/v1/jobs" | jq -r .id
}

summarize_job() {
  mode="$1"
  job_file="$2"
  jq -r --arg mode "$mode" '
    . as $job
    | ($job.result | fromjson? // {}) as $result
    | ($result.execution_result // {}) as $trace
    | {
        mode: $mode,
        job_id: $job.id,
        status: $job.status,
        rpc_endpoint_count: ($trace.rpc_endpoint_count // $result.rpc_endpoint_count // 0),
        duration_ms: ($trace.duration_ms // 0),
        total_ms: ($trace.timings.total_ms // $trace.duration_ms // 0),
        model_bytes: ($trace.model_bytes // 0),
        runtime_version: ($trace.runtime_version // $result.runtime_version // ""),
        output: (($trace.output // $result.output // "") | gsub("\n"; " ") | .[0:180])
      }' "$job_file"
}

main() {
  need curl
  need jq
  mkdir -p "$OUT_DIR"

  echo "Refreshing RPC pool..."
  curl -fsS -X POST "${curl_auth_args[@]}" "$MANAGER_URL/v1/runtime/rpc-pool/refresh?timeout_ms=1500" > "$OUT_DIR/rpc-refresh.json"
  plan="$(curl -fsS "${curl_auth_args[@]}" "$MANAGER_URL/v1/models/$MODEL_ID/distributed-rpc-plan?check=1")"
  printf '%s' "$plan" > "$OUT_DIR/rpc-plan.json"
  coordinator="$(printf '%s' "$plan" | jq -r .coordinator_node_id)"
  if [ -z "$coordinator" ] || [ "$coordinator" = "null" ]; then
    echo "error: distributed plan has no coordinator" >&2
    jq . "$OUT_DIR/rpc-plan.json" >&2
    exit 1
  fi

  endpoints_count="$(printf '%s' "$plan" | jq '.rpc_endpoints | length')"
  if [ "$endpoints_count" -lt 1 ]; then
    echo "error: no RPC endpoints available" >&2
    jq . "$OUT_DIR/rpc-plan.json" >&2
    exit 1
  fi

  echo "Running local baseline..."
  local_job="$(submit_job local "$coordinator" '[]' '{}')"
  wait_job "$local_job"
  summarize_job local "$OUT_DIR/$local_job.json" > "$OUT_DIR/local.json"

  summary="$OUT_DIR/summary.jsonl"
  cat "$OUT_DIR/local.json" > "$summary"

  max_mode="$endpoints_count"
  if [ "$max_mode" -gt 3 ]; then
    max_mode=3
  fi
  n=1
  while [ "$n" -le "$max_mode" ]; do
    endpoints="$(printf '%s' "$plan" | jq --argjson n "$n" '.rpc_endpoints[:$n]')"
    bench_plan="$(printf '%s' "$plan" | jq --argjson endpoints "$endpoints" '
      {
        id: ("benchmark-" + (now|tostring)),
        protocol: "cmesh.distributed-rpc",
        protocol_version: 1,
        plan_schema_version: 1,
        mode: .mode,
        model_id: .model_id,
        coordinator_node_id: .coordinator_node_id,
        coordinator_node_name: .coordinator_node_name,
        rpc_endpoints: $endpoints,
        backends: [.backends[] as $backend | select($endpoints | index($backend.endpoint)) | $backend],
        health_checked: .health_checked,
        planned_at: (now|todate)
      }')"
    echo "Running distributed RPC with $n backend endpoint(s)..."
    job_id="$(submit_job "rpc-$n" "$coordinator" "$endpoints" "$bench_plan")"
    wait_job "$job_id"
    summarize_job "rpc-$n" "$OUT_DIR/$job_id.json" | tee "$OUT_DIR/rpc-$n.json" >> "$summary"
    n=$((n + 1))
  done

  jq -s . "$summary" | tee "$OUT_DIR/summary.json"
  echo "Benchmark evidence: $OUT_DIR"
}

main "$@"
