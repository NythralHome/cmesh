#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-linux-production-reliability-smoke-$(date -u +%Y%m%d%H%M%S)}"
DECODE_LOOP_RUNS="${CMESH_RELIABILITY_DECODE_LOOP_RUNS:-2}"

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

run_decode_loop() {
  local index="$1"
  local dir="$WORK_DIR/decode-loop-$index"
  CMESH_CDIP_DAEMON_DECODE_LOOP_SMOKE_DIR="$dir" "$ROOT_DIR/scripts/cdip-daemon-decode-loop-smoke.sh" >"$WORK_DIR/decode-loop-$index.log" 2>&1
  [[ -f "$dir/summary.json" ]] || fail "decode loop $index did not write summary"
  jq -e '.status == "passed"' "$dir/summary.json" >/dev/null
  jq -e '.decode_steps == 3' "$dir/summary.json" >/dev/null
  jq -e '.status == "succeeded" and (.result | fromjson | .output == " token-1 token-2 token-3")' "$dir/parent.json" >/dev/null
}

main() {
  need jq
  mkdir -p "$WORK_DIR"

  for index in $(seq 1 "$DECODE_LOOP_RUNS"); do
    run_decode_loop "$index"
  done

  CMESH_CDIP_DAEMON_SESSION_RECREATE_SMOKE_DIR="$WORK_DIR/session-recreate" \
    "$ROOT_DIR/scripts/cdip-daemon-session-recreate-smoke.sh" >"$WORK_DIR/session-recreate.log" 2>&1
  [[ -f "$WORK_DIR/session-recreate/summary.json" ]] || fail "session recreate did not write summary"
  jq -e '.status == "passed" and (.recreated_sessions | length) == 2' "$WORK_DIR/session-recreate/summary.json" >/dev/null

  CMESH_CDIP_RECOVERY_CLEANUP_SMOKE_DIR="$WORK_DIR/recovery-cleanup" \
    "$ROOT_DIR/scripts/cdip-recovery-cleanup-smoke.sh" >"$WORK_DIR/recovery-cleanup.log" 2>&1
  [[ -f "$WORK_DIR/recovery-cleanup/summary.json" ]] || fail "recovery cleanup did not write summary"
  jq -e '.status == "passed"' "$WORK_DIR/recovery-cleanup/summary.json" >/dev/null

  jq -n \
    --arg evidence "$WORK_DIR" \
    --argjson decode_loop_runs "$DECODE_LOOP_RUNS" \
    '{
      status: "passed",
      evidence: $evidence,
      decode_loop_runs: $decode_loop_runs,
      checks: [
        "cdip-daemon-decode-loop-smoke",
        "cdip-daemon-session-recreate-smoke",
        "cdip-recovery-cleanup-smoke"
      ]
    }' >"$WORK_DIR/summary.json"

  echo "PASS: Linux production reliability smoke completed"
  echo "Evidence: $WORK_DIR"
}

main "$@"
