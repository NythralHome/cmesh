#!/usr/bin/env sh
set -eu

MANAGER_URL="${CMESH_MANAGER_URL:-${1:-}}"
OPERATOR_TOKEN="${CMESH_OPERATOR_TOKEN:-${2:-}}"
MODEL_ID="${CMESH_MODEL_ID:-qwen2.5-0.5b-instruct-q4-k-m}"
PROMPT="${CMESH_PROMPT:-Reply in one short sentence: CMesh distributed RPC smoke succeeded.}"
MAX_TOKENS="${CMESH_MAX_TOKENS:-48}"
TEMPERATURE="${CMESH_TEMPERATURE:-0.2}"

if [ -z "$MANAGER_URL" ]; then
  echo "missing manager URL; pass CMESH_MANAGER_URL or first argument" >&2
  exit 1
fi
if [ -z "$OPERATOR_TOKEN" ]; then
  echo "missing operator token; pass CMESH_OPERATOR_TOKEN or second argument" >&2
  exit 1
fi

MANAGER_URL="${MANAGER_URL%/}"
auth="Authorization: Bearer $OPERATOR_TOKEN"

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "$1 is required" >&2; exit 1; }
}
need curl
need jq

wait_job() {
  job_id="$1"
  i=0
  while [ "$i" -lt 120 ]; do
    payload="$(curl -fsS -H "$auth" "$MANAGER_URL/v1/jobs/$job_id")"
    status="$(printf '%s' "$payload" | jq -r '.status')"
    echo "$job_id $status"
    case "$status" in
      succeeded)
        printf '%s\n' "$payload" | jq '{id,type,status,result,error,started_at,finished_at}'
        return 0
        ;;
      failed)
        printf '%s\n' "$payload" | jq '{id,type,status,result,error,started_at,finished_at}'
        return 1
        ;;
    esac
    i=$((i + 1))
    sleep 5
  done
  echo "timed out waiting for $job_id" >&2
  return 1
}

echo "Refreshing RPC pool health..."
curl -fsS -X POST -H "$auth" "$MANAGER_URL/v1/runtime/rpc-pool/refresh?timeout_ms=1500" | jq '{summary,endpoints}'

plan="$(curl -fsS -H "$auth" "$MANAGER_URL/v1/models/$MODEL_ID/distributed-rpc-plan?check=1")"
echo "$plan" | jq '{model_id,executable_now,coordinator_node_id,coordinator_node_name,rpc_endpoints,backends,blockers,warnings}'

coordinator="$(printf '%s' "$plan" | jq -r '.coordinator_node_id')"
if [ "$coordinator" = "null" ] || [ -z "$coordinator" ]; then
  coordinator="$(curl -fsS -H "$auth" "$MANAGER_URL/v1/nodes" | jq -r '[.nodes[] | select(.status == "online") | select(any(.resources.runtimes[]?; .name == "llama.cpp" and .ready == true)) | .id][0] // ""')"
fi
if [ -z "$coordinator" ]; then
  echo "no online llama.cpp-ready coordinator worker is available" >&2
  exit 1
fi

installed="$(curl -fsS -H "$auth" "$MANAGER_URL/v1/models/$MODEL_ID" | jq -r --arg node "$coordinator" '[.model.installed[]? | select(.node_id == $node and .model_ready == true)] | length')"
if [ "$installed" = "0" ]; then
  echo "Installing $MODEL_ID on coordinator $coordinator..."
  install_job="$(curl -fsS -X POST -H "$auth" -H "Content-Type: application/json" \
    -d "{\"node_id\":\"$coordinator\"}" \
    "$MANAGER_URL/v1/models/$MODEL_ID/install")"
  install_job_id="$(printf '%s' "$install_job" | jq -r '.id')"
  wait_job "$install_job_id"
fi

plan="$(curl -fsS -H "$auth" "$MANAGER_URL/v1/models/$MODEL_ID/distributed-rpc-plan?check=1&node_id=$coordinator")"
echo "$plan" | jq '{model_id,executable_now,coordinator_node_id,coordinator_node_name,rpc_endpoints,backends,blockers,warnings}'
if [ "$(printf '%s' "$plan" | jq -r '.executable_now')" != "true" ]; then
  echo "distributed RPC plan is not executable" >&2
  exit 1
fi

echo "Submitting distributed RPC generate..."
job="$(curl -fsS -X POST -H "$auth" -H "Content-Type: application/json" \
  -d "$(jq -n --arg node "$coordinator" --arg prompt "$PROMPT" --arg temp "$TEMPERATURE" --argjson max "$MAX_TOKENS" '{node_id:$node,prompt:$prompt,max_tokens:$max,temperature:$temp}')" \
  "$MANAGER_URL/v1/models/$MODEL_ID/distributed-rpc-generate")"
job_id="$(printf '%s' "$job" | jq -r '.id')"
wait_job "$job_id"

echo "Latest distributed runs:"
curl -fsS -H "$auth" "$MANAGER_URL/v1/distributed-runs?limit=3" | jq '.runs'
