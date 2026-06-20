#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOC="$ROOT_DIR/docs/LINUX_OBSERVABILITY.md"

fail() {
  echo "error: $*" >&2
  exit 1
}

[[ -f "$DOC" ]] || fail "missing observability doc: $DOC"

required_markers=(
  "/v1/observability"
  'Authorization: Bearer $CMESH_OPERATOR_TOKEN'
  "worker resource snapshots"
  "stage daemon endpoint and readiness"
  "distributed/CDIP job summaries"
  "/v1/models/{model_id}/distributed-plan"
  "/v1/models/{model_id}/distributed-generate"
  "/v1/cdip/jobs/{job_id}/prepare"
  "/v1/cdip/jobs/{job_id}/decode-loop"
  "journalctl -u cmesh.service"
  "journalctl -u cmesh-worker.service"
  "journalctl -u cmesh-stage-daemon.service"
  "scripts/observability-smoke.sh"
)

for marker in "${required_markers[@]}"; do
  grep -F "$marker" "$DOC" >/dev/null || fail "observability doc is missing marker: $marker"
done

echo "PASS: Linux production observability doc smoke completed"
echo "doc: $DOC"
