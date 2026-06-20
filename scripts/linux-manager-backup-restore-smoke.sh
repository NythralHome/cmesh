#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-linux-manager-backup-restore-smoke-$(date -u +%Y%m%d%H%M%S)}"
JOIN_TOKEN="${CMESH_JOIN_TOKEN:-backup-restore-join-token}"
OPERATOR_TOKEN="${CMESH_OPERATOR_TOKEN:-backup-restore-operator-token}"
MANAGER_PID=""

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

free_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

wait_for_http() {
  local url="$1"
  for _ in $(seq 1 80); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  fail "timed out waiting for $url"
}

start_manager() {
  local port="$1"
  local state_path="$2"
  local log_path="$3"
  "$WORK_DIR/cmesh" manager start \
    --addr "127.0.0.1:$port" \
    --join-token "$JOIN_TOKEN" \
    --operator-token "$OPERATOR_TOKEN" \
    --state-path "$state_path" \
    --cdip-auto-advance=false \
    >"$log_path" 2>&1 &
  echo "$!"
}

main() {
  need curl
  need jq
  need python3
  mkdir -p "$WORK_DIR"

  go build -o "$WORK_DIR/cmesh" "$ROOT_DIR/cmd/cmesh"

  local state_path backup_path restore_path port manager node_id job_id
  state_path="$WORK_DIR/manager-state.json"
  backup_path="$WORK_DIR/manager-state.backup.json"
  restore_path="$WORK_DIR/manager-state.restore.json"
  port="$(free_port)"
  manager="http://127.0.0.1:$port"
  MANAGER_PID="$(start_manager "$port" "$state_path" "$WORK_DIR/manager-before-backup.log")"
  trap 'kill "${MANAGER_PID:-}" >/dev/null 2>&1 || true' EXIT
  wait_for_http "$manager/health"

  curl -fsS -X POST "$manager/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"backup-restore-worker\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":4,\"cores_allowed\":2},\"memory\":{\"total_bytes\":8589934592,\"allowed_bytes\":4294967296},\"storage\":{\"total_bytes\":21474836480,\"allowed_bytes\":10737418240,\"free_bytes\":17179869184}},\"join_token\":\"$JOIN_TOKEN\"}" \
    >"$WORK_DIR/join-worker.json"
  node_id="$(jq -r '.node_id' "$WORK_DIR/join-worker.json")"

  curl -fsS -X POST "$manager/v1/jobs" \
    -H "Authorization: Bearer $OPERATOR_TOKEN" \
    -H 'Content-Type: application/json' \
    -d '{"type":"echo","input":"backup restore smoke","requested_by":"backup-restore-smoke"}' \
    >"$WORK_DIR/job-created.json"
  job_id="$(jq -r '.id' "$WORK_DIR/job-created.json")"

  for _ in $(seq 1 40); do
    if [[ -s "$state_path" ]]; then
      break
    fi
    sleep 0.1
  done
  [[ -s "$state_path" ]] || fail "manager state file was not created"
  cp "$state_path" "$backup_path"
  cp "$backup_path" "$restore_path"
  jq -e --arg node_id "$node_id" --arg job_id "$job_id" '.nodes[$node_id] and .jobs[$job_id]' "$backup_path" >/dev/null

  kill "$MANAGER_PID" >/dev/null 2>&1 || true
  wait "$MANAGER_PID" >/dev/null 2>&1 || true

  port="$(free_port)"
  manager="http://127.0.0.1:$port"
  MANAGER_PID="$(start_manager "$port" "$restore_path" "$WORK_DIR/manager-after-restore.log")"
  wait_for_http "$manager/health"

  curl -fsS -H "Authorization: Bearer $OPERATOR_TOKEN" "$manager/v1/nodes" >"$WORK_DIR/restored-nodes.json"
  curl -fsS -H "Authorization: Bearer $OPERATOR_TOKEN" "$manager/v1/jobs" >"$WORK_DIR/restored-jobs.json"
  jq -e --arg node_id "$node_id" '.nodes | any(.id == $node_id and .name == "backup-restore-worker")' "$WORK_DIR/restored-nodes.json" >/dev/null
  jq -e --arg job_id "$job_id" '.jobs | any(.id == $job_id and .input == "backup restore smoke")' "$WORK_DIR/restored-jobs.json" >/dev/null

  jq -n \
    --arg evidence "$WORK_DIR" \
    --arg state_path "$state_path" \
    --arg backup_path "$backup_path" \
    --arg restore_path "$restore_path" \
    --arg node_id "$node_id" \
    --arg job_id "$job_id" \
    '{
      status: "passed",
      evidence: $evidence,
      state_path: $state_path,
      backup_path: $backup_path,
      restore_path: $restore_path,
      restored_node_id: $node_id,
      restored_job_id: $job_id
    }' >"$WORK_DIR/summary.json"

  echo "PASS: Linux manager backup/restore smoke completed"
  echo "Evidence: $WORK_DIR"
}

main "$@"
