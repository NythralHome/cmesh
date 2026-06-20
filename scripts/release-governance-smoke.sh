#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

fail() {
  echo "error: $*" >&2
  exit 1
}

require_file() {
  [[ -f "$ROOT_DIR/$1" ]] || fail "missing file: $1"
}

require_text() {
  local file="$1"
  local text="$2"
  grep -Fq "$text" "$ROOT_DIR/$file" || fail "$file missing required text: $text"
}

require_file LICENSE
require_file NOTICE
require_file CONTRIBUTING.md
require_file CODE_OF_CONDUCT.md
require_file SECURITY.md
require_file docs/THIRD_PARTY_NOTICES.md
require_file docs/RELEASE_SCOPE.md

require_text LICENSE "Apache License"
require_text NOTICE "CMesh contributors"
require_text NOTICE "docs/THIRD_PARTY_NOTICES.md"
require_text CONTRIBUTING.md "Do not commit generated release artifacts"
require_text CONTRIBUTING.md "docs/RELEASE_SCOPE.md"
require_text CODE_OF_CONDUCT.md "Contributor Covenant"
require_text SECURITY.md "v0.1.0-linux-rc.1"
require_text docs/THIRD_PARTY_NOTICES.md "llama.cpp"
require_text docs/THIRD_PARTY_NOTICES.md "MIT"
require_text docs/THIRD_PARTY_NOTICES.md "GGUF"
require_text docs/THIRD_PARTY_NOTICES.md "Operators are responsible"

echo "PASS: release governance smoke completed"
