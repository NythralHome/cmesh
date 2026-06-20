#!/usr/bin/env bash
set -euo pipefail

if [[ -n "${CMESH_GGUF_MODEL_PATH:-}" ]]; then
  printf '%s\n' "$CMESH_GGUF_MODEL_PATH"
  exit 0
fi

if [[ "${CMESH_DOWNLOAD_GGUF_FIXTURE:-0}" != "1" ]]; then
  exit 2
fi

FIXTURE_DIR="${CMESH_GGUF_FIXTURE_DIR:-/tmp/cmesh-gguf-fixtures}"
FIXTURE_URL="${CMESH_GGUF_FIXTURE_URL:-https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/qwen2.5-0.5b-instruct-q4_k_m.gguf}"
FIXTURE_FILE="${CMESH_GGUF_FIXTURE_FILE:-qwen2.5-0.5b-instruct-q4_k_m.gguf}"
TARGET="$FIXTURE_DIR/$FIXTURE_FILE"
PARTIAL="$TARGET.tmp"

mkdir -p "$FIXTURE_DIR"

if [[ -s "$TARGET" ]]; then
  printf '%s\n' "$TARGET"
  exit 0
fi

echo "Downloading GGUF fixture to $TARGET" >&2
rm -f "$PARTIAL"
curl -fL --retry 3 --retry-delay 2 -o "$PARTIAL" "$FIXTURE_URL"
if [[ ! -s "$PARTIAL" ]]; then
  echo "FAIL: downloaded GGUF fixture is empty" >&2
  exit 1
fi
mv "$PARTIAL" "$TARGET"
printf '%s\n' "$TARGET"
