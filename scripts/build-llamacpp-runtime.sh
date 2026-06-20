#!/usr/bin/env sh
set -eu

LLAMA_CPP_REF="${LLAMA_CPP_REF:-b9704}"
LLAMA_CPP_REPO="${LLAMA_CPP_REPO:-https://github.com/ggml-org/llama.cpp.git}"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-llama.cpp-build}"
OUT_DIR="${OUT_DIR:-dist/runtimes}"
JOBS="${JOBS:-2}"
CMESH_LLAMA_CPP_STAGE_RUNNER="${CMESH_LLAMA_CPP_STAGE_RUNNER:-false}"

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
STAGE_TOOL_DIR="$ROOT_DIR/integrations/llamacpp/cmesh-stage-runner"
PATCH_DIR="$ROOT_DIR/integrations/llamacpp/patches"

platform="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$platform" in
  darwin) os="darwin" ;;
  linux) os="linux" ;;
  *) echo "unsupported OS: $platform" >&2; exit 1 ;;
esac
case "$arch" in
  x86_64|amd64) cpu="amd64" ;;
  arm64|aarch64) cpu="arm64" ;;
  *) echo "unsupported CPU architecture: $arch" >&2; exit 1 ;;
esac

case "$os" in
  darwin) install_rpath="@loader_path;@loader_path/../lib" ;;
  linux) install_rpath='$ORIGIN;$ORIGIN/../lib' ;;
esac

runtime_flavor="rpc"
if [ "$CMESH_LLAMA_CPP_STAGE_RUNNER" = "true" ]; then
  runtime_flavor="rpc-stage"
fi

runtime_id="llama.cpp-${LLAMA_CPP_REF}-${os}-${cpu}-${runtime_flavor}"
source_dir="$WORK_DIR/src"
package_dir="$WORK_DIR/package"
archive="$OUT_DIR/$runtime_id.tar.gz"

rm -rf "$source_dir" "$package_dir"
mkdir -p "$WORK_DIR" "$package_dir/bin" "$package_dir/lib" "$OUT_DIR"

git clone --depth 1 --branch "$LLAMA_CPP_REF" "$LLAMA_CPP_REPO" "$source_dir"

if [ "$CMESH_LLAMA_CPP_STAGE_RUNNER" = "true" ]; then
  if [ ! -d "$STAGE_TOOL_DIR" ]; then
    echo "missing stage runner source: $STAGE_TOOL_DIR" >&2
    exit 1
  fi
  for patch in "$PATCH_DIR"/*.patch; do
    [ -f "$patch" ] || continue
    git -C "$source_dir" apply --check "$patch"
    git -C "$source_dir" apply "$patch"
  done

  rm -rf "$source_dir/tools/cmesh-stage-runner"
  mkdir -p "$source_dir/tools/cmesh-stage-runner"
  cp "$STAGE_TOOL_DIR/CMakeLists.txt" "$STAGE_TOOL_DIR/cmesh-stage-runner.cpp" "$source_dir/tools/cmesh-stage-runner/"

  if ! grep -q "add_subdirectory(cmesh-stage-runner)" "$source_dir/tools/CMakeLists.txt"; then
    python3 - "$source_dir/tools/CMakeLists.txt" <<'PY'
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
fi

cmake -S "$source_dir" -B "$source_dir/build" \
  -DGGML_RPC=ON \
  -DBUILD_SHARED_LIBS=ON \
  -DCMAKE_BUILD_WITH_INSTALL_RPATH=ON \
  -DCMAKE_INSTALL_RPATH="$install_rpath" \
  -DCMAKE_BUILD_TYPE=Release

targets="llama-cli rpc-server"
if [ "$CMESH_LLAMA_CPP_STAGE_RUNNER" = "true" ]; then
  targets="$targets cmesh-stage-runner"
fi

cmake --build "$source_dir/build" --target $targets -j "$JOBS"

if [ "$CMESH_LLAMA_CPP_STAGE_RUNNER" = "true" ]; then
  "$source_dir/build/bin/cmesh-stage-runner" --command resident-capabilities > "$WORK_DIR/cmesh-stage-runner-resident-capabilities.json"
  grep -F '"kind": "cmesh.llamacpp_resident_capabilities"' "$WORK_DIR/cmesh-stage-runner-resident-capabilities.json" >/dev/null ||
    { echo "cmesh-stage-runner resident-capabilities did not return expected kind" >&2; exit 1; }
  grep -F '"protocol": "cdip.llamacpp-resident-runner-v1"' "$WORK_DIR/cmesh-stage-runner-resident-capabilities.json" >/dev/null ||
    { echo "cmesh-stage-runner resident-capabilities did not return expected protocol" >&2; exit 1; }
  printf 'command=capabilities\ncommand=shutdown\n' | "$source_dir/build/bin/cmesh-stage-runner" --command resident-loop > "$WORK_DIR/cmesh-stage-runner-resident-loop.jsonl"
  grep -F '"kind":"cmesh.llamacpp_resident_loop_capabilities"' "$WORK_DIR/cmesh-stage-runner-resident-loop.jsonl" >/dev/null ||
    { echo "cmesh-stage-runner resident-loop did not return expected capabilities kind" >&2; exit 1; }
  grep -F '"protocol":"cdip.llamacpp-resident-loop-v1"' "$WORK_DIR/cmesh-stage-runner-resident-loop.jsonl" >/dev/null ||
    { echo "cmesh-stage-runner resident-loop did not return expected protocol" >&2; exit 1; }
fi

cp "$source_dir/build/bin/llama-cli" "$source_dir/build/bin/rpc-server" "$package_dir/bin/"
if [ "$CMESH_LLAMA_CPP_STAGE_RUNNER" = "true" ]; then
  cp "$source_dir/build/bin/cmesh-stage-runner" "$package_dir/bin/"
fi
find "$source_dir/build" \( -type f -o -type l \) \( -name 'lib*.so*' -o -name 'lib*.dylib' \) -exec cp -a {} "$package_dir/lib/" \;

tar -C "$package_dir" -czf "$archive" .

cat <<EOF
Built $archive

Use with workers:
  CMESH_LLAMA_CPP_RUNTIME_URL=https://your-host/$runtime_id.tar.gz
  CMESH_LLAMA_CPP_RUNTIME_NAME=$runtime_id.tar.gz
  CMESH_LLAMA_CPP_RUNTIME_VERSION=$runtime_id
  CMESH_LLAMA_CPP_PREFER_CACHE=true
EOF
