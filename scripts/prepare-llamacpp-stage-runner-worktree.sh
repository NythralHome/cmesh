#!/usr/bin/env bash
set -euo pipefail

LLAMA_CPP_REF="${LLAMA_CPP_REF:-10786217e9d40c848ac0133cbe9c5f22a52421bb}"
LLAMA_CPP_REPO="${LLAMA_CPP_REPO:-https://github.com/ggml-org/llama.cpp.git}"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-llama-stage-runner}"
SOURCE_DIR="$WORK_DIR/src"
CMESH_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAGE_TOOL_DIR="$CMESH_ROOT/integrations/llamacpp/cmesh-stage-runner"
PATCH_DIR="$CMESH_ROOT/integrations/llamacpp/patches"

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

  for patch in "$PATCH_DIR"/*.patch; do
    [ -f "$patch" ] || continue
    git -C "$SOURCE_DIR" apply --check "$patch"
    git -C "$SOURCE_DIR" apply "$patch"
  done

  rm -rf "$SOURCE_DIR/tools/cmesh-stage-runner"
  mkdir -p "$SOURCE_DIR/tools/cmesh-stage-runner"
  cp "$STAGE_TOOL_DIR/CMakeLists.txt" "$STAGE_TOOL_DIR/cmesh-stage-runner.cpp" "$SOURCE_DIR/tools/cmesh-stage-runner/"

  if ! grep -q "add_subdirectory(cmesh-stage-runner)" "$SOURCE_DIR/tools/CMakeLists.txt"; then
    python3 - "$SOURCE_DIR/tools/CMakeLists.txt" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
text = path.read_text()
needle = "    add_subdirectory(results)\n"
insert = "    add_subdirectory(cmesh-stage-runner)\n"
if insert not in text:
    if needle not in text:
        raise SystemExit("could not find insertion point in tools/CMakeLists.txt")
    text = text.replace(needle, insert + needle)
path.write_text(text)
PY
  fi

  cmake -S "$SOURCE_DIR" -B "$SOURCE_DIR/build-cmesh-stage" \
    -DGGML_RPC=ON \
    -DGGML_NATIVE=OFF \
    -DCMAKE_BUILD_TYPE=Release

  cmake --build "$SOURCE_DIR/build-cmesh-stage" --target cmesh-stage-runner -j "${JOBS:-2}"

  "$SOURCE_DIR/build-cmesh-stage/bin/cmesh-stage-runner" --probe || true

  cat <<EOF
Prepared llama.cpp CMesh stage-runner worktree

Source: $SOURCE_DIR
Binary: $SOURCE_DIR/build-cmesh-stage/bin/cmesh-stage-runner
Ref:    $LLAMA_CPP_REF
EOF
}

main "$@"
