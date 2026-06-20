#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GUIDE="$ROOT_DIR/docs/LINUX_PRODUCTION.md"
README="$ROOT_DIR/README.md"
MATRIX="$ROOT_DIR/docs/LINUX_MODEL_MATRIX.md"
MILESTONES="$ROOT_DIR/docs/LINUX_PRODUCTION_MILESTONES.md"

fail() {
  echo "error: $*" >&2
  exit 1
}

require_file() {
  [[ -f "$1" ]] || fail "missing file: $1"
}

require_text() {
  local file="$1"
  local text="$2"
  grep -Fq "$text" "$file" || fail "$file missing required text: $text"
}

reject_text() {
  local file="$1"
  local text="$2"
  if grep -Fq "$text" "$file"; then
    fail "$file contains forbidden text: $text"
  fi
}

main() {
  require_file "$GUIDE"
  require_file "$README"
  require_file "$MATRIX"
  require_file "$MILESTONES"

  require_text "$GUIDE" 'Platform: `linux/amd64`'
  require_text "$GUIDE" "systemd"
  require_text "$GUIDE" "qwen2.5-14b-instruct-q4-k-m"
  require_text "$GUIDE" "Qwen2.5-14B-Instruct-Q4_K_M.gguf"
  require_text "$GUIDE" "CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM=true"
  require_text "$GUIDE" "VERSION=v0.1.0-linux-rc.1"
  require_text "$GUIDE" "https://github.com/NythralHome/cmesh/releases/download"
  require_text "$GUIDE" "v0.1.0-linux-rc.1.tar.gz.public-key.pem"
  require_text "$GUIDE" "openssl dgst -sha256"
  require_text "$GUIDE" "manifest.json.sig"
  require_text "$GUIDE" "checksums.txt.sig"
  require_text "$GUIDE" "memory_disk_weighted_layers"
  require_text "$GUIDE" "Not Production-Supported Yet"
  require_text "$GUIDE" "Windows sliced execution"
  require_text "$GUIDE" "macOS sliced execution"
  require_text "$GUIDE" "/tmp/cmesh-linux-beta-deployment-20260620135153"
  require_text "$GUIDE" "instances were terminated"

  require_text "$README" "docs/LINUX_PRODUCTION.md"
  require_text "$README" "Linux Production"
  require_text "$README" "current support matrix"
  require_text "$README" "VERSION=v0.1.0-linux-rc.1"
  require_text "$README" "v0.1.0-linux-rc.1.tar.gz"
  require_text "$README" "CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM=true"

  require_text "$MATRIX" "/tmp/cmesh-linux-beta-deployment-20260620135153/sliced"
  require_text "$MILESTONES" "[DONE] LP11. Real beta deployment"
  require_text "$MILESTONES" "[DONE] LP12. Public production docs"

  reject_text "$GUIDE" "Windows production is supported"
  reject_text "$GUIDE" "macOS production is supported"

  echo "PASS: Linux production public docs smoke completed"
  echo "guide: $GUIDE"
}

main "$@"
