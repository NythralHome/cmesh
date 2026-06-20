#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUNBOOK="$ROOT_DIR/docs/LINUX_SLICED_RUNBOOK.md"

fail() {
  echo "error: $*" >&2
  exit 1
}

[[ -f "$RUNBOOK" ]] || fail "missing runbook: $RUNBOOK"

required_markers=(
  "qwen2.5-14b-instruct-q4-k-m"
  "d989c91de35f32c18bdb8bec96a4b9fff2c3e5bca066503c63a5ca54dd537a4b"
  "llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz"
  "CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM=true"
  "CMESH_STAGE_DAEMON_BACKEND=llama.cpp-resident"
  "/v1/models/qwen2.5-14b-instruct-q4-k-m/distributed-plan"
  "/v1/models/qwen2.5-14b-instruct-q4-k-m/distributed-generate"
  '/v1/cdip/jobs/$PARENT_JOB_ID/prepare'
  '/v1/cdip/jobs/$PARENT_JOB_ID/decode-loop'
  "memory_disk_weighted_layers"
  "systemctl is-active cmesh.service"
  "systemctl is-active cmesh-worker.service"
  "systemctl is-active cmesh-stage-daemon.service"
  "journalctl -u cmesh-stage-daemon.service"
  "docs/LINUX_MODEL_MATRIX.md"
  "checksums.txt"
)

for marker in "${required_markers[@]}"; do
  grep -F "$marker" "$RUNBOOK" >/dev/null || fail "runbook is missing marker: $marker"
done

if grep -F "/Volumes/Devspace" "$RUNBOOK" >/dev/null || grep -F "go run" "$RUNBOOK" >/dev/null; then
  fail "runbook must not depend on local dev paths or go run"
fi

echo "PASS: Linux production sliced runbook smoke completed"
echo "runbook: $RUNBOOK"
