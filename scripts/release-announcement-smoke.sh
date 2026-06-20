#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOC="$ROOT_DIR/docs/RELEASE_ANNOUNCEMENT_v0.1.0-linux-rc.1.md"

fail() {
  echo "error: $*" >&2
  exit 1
}

[[ -f "$DOC" ]] || fail "missing announcement draft: $DOC"

markers=(
  "v0.1.0 Linux RC1"
  "Linux-first distributed AI compute"
  "Signed Linux release tarball"
  "qwen2.5-14b-instruct-q4-k-m"
  "It does not support Windows or macOS production installs yet"
  "It does not support arbitrary model slicing"
  "v0.1.0-linux-rc.1.tar.gz"
  "CONTRIBUTING.md"
  "SECURITY.md"
  "v0.1.1 Linux stabilization"
)

for marker in "${markers[@]}"; do
  grep -Fq "$marker" "$DOC" || fail "announcement missing marker: $marker"
done

echo "PASS: release announcement smoke completed"
