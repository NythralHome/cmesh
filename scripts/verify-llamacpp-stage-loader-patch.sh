#!/usr/bin/env bash
set -euo pipefail

LLAMA_CPP_REF="${LLAMA_CPP_REF:-10786217e9d40c848ac0133cbe9c5f22a52421bb}"
LLAMA_CPP_REPO="${LLAMA_CPP_REPO:-https://github.com/ggml-org/llama.cpp.git}"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-llama-stage-loader-patch}"
SOURCE_DIR="$WORK_DIR/src"
CMESH_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PATCH_FILE="$CMESH_ROOT/integrations/llamacpp/patches/0001-cmesh-stage-selected-tensor-load-plumbing.patch"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: $1 is required" >&2
    exit 1
  }
}

main() {
  need git
  need cmake

  mkdir -p "$WORK_DIR"
  if [ ! -d "$SOURCE_DIR/.git" ]; then
    git clone "$LLAMA_CPP_REPO" "$SOURCE_DIR"
  fi

  git -C "$SOURCE_DIR" fetch --depth 1 origin "$LLAMA_CPP_REF"
  git -C "$SOURCE_DIR" checkout --detach "$LLAMA_CPP_REF"
  git -C "$SOURCE_DIR" reset --hard "$LLAMA_CPP_REF"
  git -C "$SOURCE_DIR" clean -fd
  git -C "$SOURCE_DIR" apply --check "$PATCH_FILE"
  git -C "$SOURCE_DIR" apply "$PATCH_FILE"

  cmake -S "$SOURCE_DIR" -B "$SOURCE_DIR/build-cmesh-stage-loader" \
    -DGGML_RPC=ON \
    -DCMAKE_BUILD_TYPE=Release

  cmake --build "$SOURCE_DIR/build-cmesh-stage-loader" --target llama -j "${JOBS:-2}"

  cat <<EOF
PASS: llama.cpp CMesh stage loader patch applies and builds

Source: $SOURCE_DIR
Ref:    $LLAMA_CPP_REF
Patch:  $PATCH_FILE
EOF
}

main "$@"
