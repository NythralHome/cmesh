#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LLAMA_CPP_REF="${LLAMA_CPP_REF:-b9704}"
TARGET_OS="${CMESH_STAGE_RUNTIME_TARGET_OS:-linux}"
TARGET_CPU="${CMESH_STAGE_RUNTIME_TARGET_CPU:-amd64}"
TARGET_DIR="${CMESH_STAGE_RUNTIME_CURRENT_DIR:-$ROOT_DIR/dist/runtimes-$TARGET_OS-$TARGET_CPU-current}"
TARGET_ARCHIVE="${CMESH_STAGE_RUNTIME_TARGET_ARCHIVE:-$TARGET_DIR/llama.cpp-$LLAMA_CPP_REF-$TARGET_OS-$TARGET_CPU-rpc-stage.tar.gz}"
SOURCE_ARCHIVE="${CMESH_STAGE_RUNTIME_ARCHIVE:-}"
SOURCE_URL="${CMESH_STAGE_RUNTIME_URL:-}"

fail() {
  echo "error: $*" >&2
  exit 1
}

abs_path() {
  local path="$1"
  local dir base
  dir="$(dirname "$path")"
  base="$(basename "$path")"
  printf '%s/%s\n' "$(cd "$dir" && pwd -P)" "$base"
}

host_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
host_cpu="$(uname -m)"
case "$host_cpu" in
  x86_64|amd64) host_cpu=amd64 ;;
  arm64|aarch64) host_cpu=arm64 ;;
esac

mkdir -p "$TARGET_DIR"

if [[ -n "$SOURCE_ARCHIVE" && -n "$SOURCE_URL" ]]; then
  fail "set only one of CMESH_STAGE_RUNTIME_ARCHIVE or CMESH_STAGE_RUNTIME_URL"
fi

if [[ -n "$SOURCE_ARCHIVE" ]]; then
  [[ -f "$SOURCE_ARCHIVE" ]] || fail "CMESH_STAGE_RUNTIME_ARCHIVE does not exist: $SOURCE_ARCHIVE"
  if [[ "$(abs_path "$SOURCE_ARCHIVE")" != "$(abs_path "$TARGET_ARCHIVE")" ]]; then
    cp "$SOURCE_ARCHIVE" "$TARGET_ARCHIVE"
  fi
elif [[ -n "$SOURCE_URL" ]]; then
  curl -fL --retry 3 --retry-delay 2 -o "$TARGET_ARCHIVE.tmp" "$SOURCE_URL"
  mv "$TARGET_ARCHIVE.tmp" "$TARGET_ARCHIVE"
elif [[ -f "$TARGET_ARCHIVE" ]]; then
  :
elif [[ -f "$ROOT_DIR/dist/runtimes/$(basename "$TARGET_ARCHIVE")" ]]; then
  cp "$ROOT_DIR/dist/runtimes/$(basename "$TARGET_ARCHIVE")" "$TARGET_ARCHIVE"
elif [[ -f "$ROOT_DIR/dist/runtimes-$TARGET_OS-$TARGET_CPU-stage/$(basename "$TARGET_ARCHIVE")" ]]; then
  cp "$ROOT_DIR/dist/runtimes-$TARGET_OS-$TARGET_CPU-stage/$(basename "$TARGET_ARCHIVE")" "$TARGET_ARCHIVE"
elif [[ "$host_os" == "$TARGET_OS" && "$host_cpu" == "$TARGET_CPU" ]]; then
  (
    cd "$ROOT_DIR"
    LLAMA_CPP_REF="$LLAMA_CPP_REF" OUT_DIR="$TARGET_DIR" CMESH_LLAMA_CPP_STAGE_RUNNER=true scripts/build-llamacpp-runtime.sh
  )
else
  fail "missing $TARGET_OS/$TARGET_CPU stage runtime archive: $TARGET_ARCHIVE

Provide one of:
  CMESH_STAGE_RUNTIME_ARCHIVE=/path/to/$(basename "$TARGET_ARCHIVE") $0
  CMESH_STAGE_RUNTIME_URL=https://.../$(basename "$TARGET_ARCHIVE") $0

This host is $host_os/$host_cpu, so it cannot build the required $TARGET_OS/$TARGET_CPU artifact natively."
fi

"$ROOT_DIR/scripts/verify-llamacpp-runtime-artifact.sh" "$TARGET_ARCHIVE" >/dev/null

if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$TARGET_ARCHIVE" > "$TARGET_ARCHIVE.sha256"
else
  shasum -a 256 "$TARGET_ARCHIVE" > "$TARGET_ARCHIVE.sha256"
fi

echo "$TARGET_ARCHIVE"
