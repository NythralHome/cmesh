#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CMESH_LINUX_PACKAGE_DIR="${CMESH_LINUX_PACKAGE_DIR:-${1:-}}"
CMESH_BETA_EVIDENCE_DIR="${CMESH_BETA_EVIDENCE_DIR:-/tmp/cmesh-linux-beta-deployment-20260620135153}"
CMESH_LAUNCH_GATE_DIR="${CMESH_LAUNCH_GATE_DIR:-${TMPDIR:-/tmp}/cmesh-linux-production-launch-gate-$(date -u +%Y%m%d%H%M%S)}"

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

require_file() {
  [[ -f "$1" ]] || fail "missing file: $1"
}

verify_beta_evidence() {
  local root="$1"
  require_file "$root/summary.json"
  require_file "$root/installers/exit_code"
  require_file "$root/installers/cleanup-instances.json"
  require_file "$root/sliced/exit_code"
  require_file "$root/sliced/cleanup-instances.json"
  require_file "$root/sliced/summary.json"
  require_file "$root/sliced/memory-aware-placement-plan.json"

  jq -e '.status == "passed"' "$root/summary.json" >/dev/null
  [[ "$(cat "$root/installers/exit_code")" == "0" ]] || fail "installer AWS evidence exit_code is not 0"
  [[ "$(cat "$root/sliced/exit_code")" == "0" ]] || fail "sliced AWS evidence exit_code is not 0"
  jq -e 'all(.State == "terminated") and length == 3' "$root/installers/cleanup-instances.json" >/dev/null
  jq -e 'all(.State == "terminated") and length == 3' "$root/sliced/cleanup-instances.json" >/dev/null
  jq -e '
    .status == "succeeded" and
    .memory_pressure_placement_verified == true and
    .memory_pressure_execution_verified == true and
    .n_layer == 48 and
    (.placement_proof.stages | length) == 3 and
    (.placement_proof.required_memory_bytes > ([.placement_proof.stages[].allowed_memory_bytes] | max)) and
    .result.resident_kv_in_memory == true and
    .dispatch_result.resident_kv_in_memory == true and
    .dispatch_result.step == 3
  ' "$root/sliced/summary.json" >/dev/null
}

main() {
  need jq
  need go
  need docker
  need openssl
  need shasum

  [[ -n "$CMESH_LINUX_PACKAGE_DIR" ]] || fail "CMESH_LINUX_PACKAGE_DIR is required"
  CMESH_LINUX_PACKAGE_DIR="$(cd "$CMESH_LINUX_PACKAGE_DIR" && pwd -P)"
  [[ -d "$CMESH_LINUX_PACKAGE_DIR" ]] || fail "package dir does not exist: $CMESH_LINUX_PACKAGE_DIR"
  [[ -d "$CMESH_BETA_EVIDENCE_DIR" ]] || fail "beta evidence dir does not exist: $CMESH_BETA_EVIDENCE_DIR"

  mkdir -p "$CMESH_LAUNCH_GATE_DIR"
  jq -n \
    --arg package_dir "$CMESH_LINUX_PACKAGE_DIR" \
    --arg beta_evidence_dir "$CMESH_BETA_EVIDENCE_DIR" \
    --arg launch_gate_dir "$CMESH_LAUNCH_GATE_DIR" \
    '{package_dir:$package_dir,beta_evidence_dir:$beta_evidence_dir,launch_gate_dir:$launch_gate_dir}' \
    > "$CMESH_LAUNCH_GATE_DIR/config.json"

  verify_beta_evidence "$CMESH_BETA_EVIDENCE_DIR"
  CMESH_LINUX_PACKAGE_DIR="$CMESH_LINUX_PACKAGE_DIR" "$ROOT_DIR/scripts/linux-stable-release-smoke.sh" > "$CMESH_LAUNCH_GATE_DIR/stable-release-smoke.txt"
  CMESH_LINUX_PACKAGE_DIR="$CMESH_LINUX_PACKAGE_DIR" "$ROOT_DIR/scripts/linux-production-docs-smoke.sh" > "$CMESH_LAUNCH_GATE_DIR/docs-smoke.txt"
  CMESH_LINUX_PACKAGE_DIR="$CMESH_LINUX_PACKAGE_DIR" "$ROOT_DIR/scripts/linux-fresh-user-validation-smoke.sh" > "$CMESH_LAUNCH_GATE_DIR/fresh-user-smoke.txt"
  CMESH_LINUX_PACKAGE_DIR="$CMESH_LINUX_PACKAGE_DIR" "$ROOT_DIR/scripts/linux-production-manager-install-smoke.sh" > "$CMESH_LAUNCH_GATE_DIR/manager-install-smoke.txt"
  CMESH_LINUX_PACKAGE_DIR="$CMESH_LINUX_PACKAGE_DIR" "$ROOT_DIR/scripts/linux-production-worker-install-smoke.sh" > "$CMESH_LAUNCH_GATE_DIR/worker-install-smoke.txt"
  CMESH_LINUX_PACKAGE_DIR="$CMESH_LINUX_PACKAGE_DIR" "$ROOT_DIR/scripts/linux-production-runtime-smoke.sh" > "$CMESH_LAUNCH_GATE_DIR/runtime-smoke.txt"
  "$ROOT_DIR/scripts/linux-production-runbook-smoke.sh" > "$CMESH_LAUNCH_GATE_DIR/runbook-smoke.txt"
  "$ROOT_DIR/scripts/linux-production-security-doc-smoke.sh" > "$CMESH_LAUNCH_GATE_DIR/security-doc-smoke.txt"
  "$ROOT_DIR/scripts/linux-production-observability-doc-smoke.sh" > "$CMESH_LAUNCH_GATE_DIR/observability-doc-smoke.txt"
  "$ROOT_DIR/scripts/linux-manager-backup-restore-smoke.sh" > "$CMESH_LAUNCH_GATE_DIR/backup-restore-smoke.txt"
  "$ROOT_DIR/scripts/linux-production-reliability-smoke.sh" > "$CMESH_LAUNCH_GATE_DIR/reliability-smoke.txt"
  (cd "$ROOT_DIR" && go test ./internal/models ./internal/manager ./cmd/cmesh) > "$CMESH_LAUNCH_GATE_DIR/go-test.txt"
  (cd "$ROOT_DIR" && git diff --check) > "$CMESH_LAUNCH_GATE_DIR/git-diff-check.txt"

  cat > "$CMESH_LAUNCH_GATE_DIR/summary.txt" <<EOF
PASS: CMesh Linux production launch gate completed
package_dir: $CMESH_LINUX_PACKAGE_DIR
beta_evidence_dir: $CMESH_BETA_EVIDENCE_DIR
launch_gate_dir: $CMESH_LAUNCH_GATE_DIR
validated:
- signed release package and tarball
- public production docs
- fresh-user signed tarball flow
- manager installer dry-run
- worker installer dry-run
- runtime artifact verification
- sliced runbook
- security and observability docs
- backup/restore
- repeated local reliability
- AWS installer and sliced beta evidence with cleanup
- Go regression tests
- git diff whitespace check
EOF
  cat "$CMESH_LAUNCH_GATE_DIR/summary.txt"
}

main "$@"
