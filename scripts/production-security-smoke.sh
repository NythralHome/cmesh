#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_PRODUCTION_SECURITY_SMOKE_DIR:-${TMPDIR:-/tmp}/cmesh-production-security-smoke-$(date -u +%Y%m%d%H%M%S)}"
PORT="${CMESH_PRODUCTION_SECURITY_SMOKE_PORT:-$((19080 + RANDOM % 1000))}"
MANAGER="http://127.0.0.1:${PORT}"
PUBLIC_URL="http://127.0.0.1:${PORT}"
CONTROL_PORT="${CMESH_PRODUCTION_SECURITY_CONTROL_PORT:-$((21080 + RANDOM % 1000))}"
CONTROL_URL="http://127.0.0.1:${CONTROL_PORT}"
JOIN_TOKEN="join-smoke-token"
OPERATOR_TOKEN="operator-smoke-token"
CONTROL_TOKEN="control-smoke-token"
MANAGER_PID=""
CONTROL_PID=""

cleanup() {
  if [[ -n "${CONTROL_PID:-}" ]]; then
    kill "$CONTROL_PID" >/dev/null 2>&1 || true
    wait "$CONTROL_PID" >/dev/null 2>&1 || true
  fi
  if [[ -n "${MANAGER_PID:-}" ]]; then
    kill "$MANAGER_PID" >/dev/null 2>&1 || true
    wait "$MANAGER_PID" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

wait_for_http() {
  local url="$1"
  local i
  for i in $(seq 1 80); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

http_status() {
  local method="$1"
  local url="$2"
  local token="${3:-}"
  local body="${4:-}"
  if [[ -n "$body" ]]; then
    curl -sS -o /dev/null -w '%{http_code}' -X "$method" \
      ${token:+-H "X-CMesh-Worker-Token: $token"} \
      -H 'Content-Type: application/json' \
      -d "$body" \
      "$url"
  else
    curl -sS -o /dev/null -w '%{http_code}' -X "$method" \
      ${token:+-H "X-CMesh-Worker-Token: $token"} \
      "$url"
  fi
}

operator_status() {
  local method="$1"
  local url="$2"
  local token="${3:-}"
  local body="${4:-}"
  if [[ -n "$body" ]]; then
    curl -sS -o /dev/null -w '%{http_code}' -X "$method" \
      ${token:+-H "X-CMesh-Operator-Token: $token"} \
      -H 'Content-Type: application/json' \
      -d "$body" \
      "$url"
  else
    curl -sS -o /dev/null -w '%{http_code}' -X "$method" \
      ${token:+-H "X-CMesh-Operator-Token: $token"} \
      "$url"
  fi
}

join_status() {
  local token_json="$1"
  curl -sS -o /dev/null -w '%{http_code}' -X POST "$MANAGER/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"join-negative-worker\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":2,\"cores_allowed\":1}},\"join_token\":$token_json}"
}

join_worker() {
  local node_name="$1"
  curl -fsS -X POST "$MANAGER/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"$node_name\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":4,\"cores_allowed\":2},\"memory\":{\"total_bytes\":8589934592,\"allowed_bytes\":2147483648},\"storage\":{\"total_bytes\":107374182400,\"allowed_bytes\":10737418240,\"free_bytes\":53687091200}},\"join_token\":\"$JOIN_TOKEN\"}"
}

control_status() {
  local path="$1"
  local token="${2:-}"
  curl -sS -o /dev/null -w '%{http_code}' \
    ${token:+-H "X-CMesh-Control-Token: $token"} \
    "$CONTROL_URL$path"
}

main() {
  mkdir -p "$RUN_DIR"
  local bin="$RUN_DIR/cmesh"
  (cd "$ROOT_DIR" && go build -o "$bin" ./cmd/cmesh)

  set +e
  "$bin" manager start \
    --memory \
    --addr "0.0.0.0:${PORT}" \
    --public-url "$PUBLIC_URL" \
    >"$RUN_DIR/insecure-public-manager.log" 2>&1
  local insecure_rc=$?
  set -e
  if [[ "$insecure_rc" -eq 0 ]]; then
    fail "public manager without tokens unexpectedly started"
  fi
  grep -F "refusing to start public manager" "$RUN_DIR/insecure-public-manager.log" >/dev/null ||
    fail "public manager failure did not explain token requirement"

  "$bin" manager start \
    --memory \
    --addr "127.0.0.1:${PORT}" \
    --public-url "$PUBLIC_URL" \
    --join-token "$JOIN_TOKEN" \
    --operator-token "$OPERATOR_TOKEN" \
    --cdip-auto-advance=false \
    >"$RUN_DIR/manager.log" 2>&1 &
  MANAGER_PID=$!
  wait_for_http "$MANAGER/health" || fail "manager did not become healthy"

  "$bin" worker control \
    --addr "127.0.0.1:${CONTROL_PORT}" \
    --config "$RUN_DIR/worker-control.json" \
    --token "$CONTROL_TOKEN" \
    >"$RUN_DIR/worker-control.log" 2>&1 &
  CONTROL_PID=$!
  wait_for_http "$CONTROL_URL/health" || fail "worker control API did not become healthy"
  [[ "$(control_status /v1/status)" == "401" ]] ||
    fail "worker control status without app token was not rejected"
  [[ "$(control_status /v1/status wrong-control-token)" == "401" ]] ||
    fail "worker control status with wrong app token was not rejected"
  [[ "$(control_status /v1/status "$CONTROL_TOKEN")" == "200" ]] ||
    fail "worker control status with correct app token did not pass"

  [[ "$(operator_status GET "$MANAGER/v1/nodes")" == "401" ]] ||
    fail "operator endpoint without token was not rejected"
  [[ "$(operator_status GET "$MANAGER/v1/nodes" "wrong-operator-token")" == "401" ]] ||
    fail "operator endpoint with wrong token was not rejected"
  [[ "$(operator_status GET "$MANAGER/v1/jobs" "$JOIN_TOKEN")" == "401" ]] ||
    fail "join token was accepted as operator token"

  [[ "$(join_status null)" == "401" ]] ||
    fail "join without token was not rejected"
  [[ "$(join_status '"wrong-join-token"')" == "401" ]] ||
    fail "join with wrong token was not rejected"
  [[ "$(join_status '"operator-smoke-token"')" == "401" ]] ||
    fail "operator token was accepted as join token"

  local join_response node_id node_auth_token join_response_2 node_id_2 node_auth_token_2
  join_response="$(join_worker "security-smoke-worker-a")"
  echo "$join_response" >"$RUN_DIR/join.json"
  node_id="$(jq -r '.node_id' <<<"$join_response")"
  node_auth_token="$(jq -r '.node_auth_token' <<<"$join_response")"
  [[ -n "$node_id" && "$node_id" != "null" ]] || fail "join did not return node_id"
  [[ -n "$node_auth_token" && "$node_auth_token" != "null" ]] || fail "join did not return node_auth_token"

  join_response_2="$(join_worker "security-smoke-worker-b")"
  echo "$join_response_2" >"$RUN_DIR/join-2.json"
  node_id_2="$(jq -r '.node_id' <<<"$join_response_2")"
  node_auth_token_2="$(jq -r '.node_auth_token' <<<"$join_response_2")"
  [[ -n "$node_id_2" && "$node_id_2" != "null" ]] || fail "second join did not return node_id"
  [[ -n "$node_auth_token_2" && "$node_auth_token_2" != "null" ]] || fail "second join did not return node_auth_token"
  [[ "$node_id" != "$node_id_2" ]] || fail "workers unexpectedly received the same node id"
  [[ "$node_auth_token" != "$node_auth_token_2" ]] || fail "workers unexpectedly received the same auth token"

  curl -fsS -H "X-CMesh-Operator-Token: $OPERATOR_TOKEN" "$MANAGER/v1/nodes" >"$RUN_DIR/nodes.json"
  if grep -F "$node_auth_token" "$RUN_DIR/nodes.json" >/dev/null ||
    grep -F "$node_auth_token_2" "$RUN_DIR/nodes.json" >/dev/null ||
    grep -F "node_auth_token" "$RUN_DIR/nodes.json" >/dev/null; then
    fail "worker node auth token leaked through /v1/nodes"
  fi

  local heartbeat_body
  heartbeat_body="{\"node_id\":\"$node_id\",\"resources\":{\"cpu\":{\"cores_total\":4,\"cores_allowed\":2}}}"
  [[ "$(http_status POST "$MANAGER/v1/workers/heartbeat" "" "$heartbeat_body")" == "401" ]] ||
    fail "heartbeat without worker token was not rejected"
  [[ "$(http_status POST "$MANAGER/v1/workers/heartbeat" "wrong-token" "$heartbeat_body")" == "401" ]] ||
    fail "heartbeat with wrong worker token was not rejected"
  [[ "$(http_status POST "$MANAGER/v1/workers/heartbeat" "$node_auth_token" "$heartbeat_body")" == "200" ]] ||
    fail "heartbeat with correct worker token did not pass"

  local heartbeat_body_2
  heartbeat_body_2="{\"node_id\":\"$node_id_2\",\"resources\":{\"cpu\":{\"cores_total\":4,\"cores_allowed\":2}}}"
  [[ "$(http_status POST "$MANAGER/v1/workers/heartbeat" "$node_auth_token" "$heartbeat_body_2")" == "401" ]] ||
    fail "worker token for node A was accepted for node B heartbeat"

  [[ "$(http_status GET "$MANAGER/v1/workers/$node_id/jobs/next")" == "401" ]] ||
    fail "job poll without worker token was not rejected"
  [[ "$(http_status GET "$MANAGER/v1/workers/$node_id_2/jobs/next" "$node_auth_token")" == "401" ]] ||
    fail "worker token for node A was accepted for node B job poll"
  [[ "$(http_status GET "$MANAGER/v1/workers/$node_id/jobs/next" "$node_auth_token")" == "200" ]] ||
    fail "job poll with correct worker token did not pass"

  local leave_body leave_body_2
  leave_body_2="{\"node_id\":\"$node_id_2\"}"
  [[ "$(http_status POST "$MANAGER/v1/workers/leave" "$node_auth_token" "$leave_body_2")" == "401" ]] ||
    fail "worker token for node A was accepted for node B leave"
  leave_body="{\"node_id\":\"$node_id\"}"
  [[ "$(http_status POST "$MANAGER/v1/workers/leave" "$node_auth_token" "$leave_body")" == "200" ]] ||
    fail "leave with correct worker token did not pass"
  [[ "$(http_status POST "$MANAGER/v1/workers/leave" "$node_auth_token_2" "$leave_body_2")" == "200" ]] ||
    fail "second leave with correct worker token did not pass"

  cat >"$RUN_DIR/summary.txt" <<EOF
PASS: production security smoke succeeded
manager: $MANAGER
worker_control: $CONTROL_URL
node_id: $node_id
node_id_2: $node_id_2
evidence: $RUN_DIR
EOF
  cat "$RUN_DIR/summary.txt"
}

main "$@"
