#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOC="$ROOT_DIR/docs/POST_RELEASE_MONITORING.md"

fail() {
  echo "error: $*" >&2
  exit 1
}

[[ -f "$DOC" ]] || fail "missing post-release monitoring doc: $DOC"

markers=(
  "v0.1.0-linux-rc.1"
  "Download all assets from GitHub"
  "Verify tarball signature"
  "GitHub Security Advisories"
  "signature verification"
  "runtime auto-management"
  "sliced runbook execution"
  "i-0fb17b90444f014d7"
  "i-0790f2acd499fd88c"
  "sg-0bf0af54f02d3311d"
  "All listed resources were cleaned up"
)

for marker in "${markers[@]}"; do
  grep -Fq "$marker" "$DOC" || fail "post-release monitoring doc missing marker: $marker"
done

echo "PASS: post-release monitoring smoke completed"
