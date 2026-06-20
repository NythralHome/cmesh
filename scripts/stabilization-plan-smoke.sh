#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOC="$ROOT_DIR/docs/V0_1_1_STABILIZATION_PLAN.md"

fail() {
  echo "error: $*" >&2
  exit 1
}

[[ -f "$DOC" ]] || fail "missing stabilization plan: $DOC"

markers=(
  "v0.1.0-linux-rc.1"
  "Priority 0: Release Integrity"
  "Priority 1: Installer Reliability"
  "Priority 2: Runtime And Sliced Execution Recovery"
  "Priority 3: Documentation And User Support"
  "Priority 4: Security Hardening"
  "Deferred From v0.1.1"
  "Windows production installer"
  "macOS production installer"
  "Cross-Platform Research Track"
)

for marker in "${markers[@]}"; do
  grep -Fq "$marker" "$DOC" || fail "stabilization plan missing marker: $marker"
done

echo "PASS: stabilization plan smoke completed"
