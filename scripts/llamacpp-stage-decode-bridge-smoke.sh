#!/usr/bin/env bash
set -euo pipefail

CMESH_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODEL_PATH="${CMESH_GGUF_MODEL_PATH:-}"
STAGE_START="${CMESH_STAGE_START:-1}"
STAGE_END="${CMESH_STAGE_END:-$STAGE_START}"
OUT_DIR="${OUT_DIR:-${TMPDIR:-/tmp}/cmesh-llamacpp-stage-decode-bridge-$(date +%Y%m%d%H%M%S)}"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-llama-stage-decode-bridge}"
RUNNER_BIN="${CMESH_STAGE_RUNNER_BIN:-$WORK_DIR/src/build-cmesh-stage/bin/cmesh-stage-runner}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: $1 is required" >&2
    exit 1
  }
}

main() {
  need jq
  need python3

  if [ -z "$MODEL_PATH" ]; then
    echo "SKIP: set CMESH_GGUF_MODEL_PATH=/path/to/qwen.gguf to run the decode bridge smoke"
    exit 0
  fi
  if [ ! -f "$MODEL_PATH" ]; then
    echo "error: model not found: $MODEL_PATH" >&2
    exit 1
  fi

  if [ ! -x "$RUNNER_BIN" ]; then
    WORK_DIR="$WORK_DIR" "$CMESH_ROOT/scripts/prepare-llamacpp-stage-runner-worktree.sh"
  fi

  mkdir -p "$OUT_DIR"

  "$RUNNER_BIN" \
    --command prepare \
    --model "$MODEL_PATH" \
    --stage-start "$STAGE_START" \
    --stage-end "$STAGE_END" \
    --stage-index 1 \
    --materialize-selected-tensors \
    > "$OUT_DIR/prepare.json"

  jq -e '.status == "metadata_ready" and .selected_tensor_materialization_ready == true' "$OUT_DIR/prepare.json" >/dev/null
  n_embd="$(jq -r '.n_embd' "$OUT_DIR/prepare.json")"
  if [ -z "$n_embd" ] || [ "$n_embd" = "null" ] || [ "$n_embd" -le 0 ]; then
    echo "error: invalid n_embd from prepare report" >&2
    exit 1
  fi

  python3 - "$OUT_DIR/in.f32" "$n_embd" <<'PY'
from pathlib import Path
import struct
import sys

path = Path(sys.argv[1])
n_embd = int(sys.argv[2])
payload = b"".join(struct.pack("<f", 0.0) for _ in range(n_embd))
path.write_bytes(payload)
PY

  "$RUNNER_BIN" \
    --command decode \
    --model "$MODEL_PATH" \
    --stage-start "$STAGE_START" \
    --stage-end "$STAGE_END" \
    --stage-index 1 \
    --activation-file "$OUT_DIR/in.f32" \
    --dtype f32 \
    --shape "1,1,$n_embd" \
    --output-file "$OUT_DIR/out.f32" \
    > "$OUT_DIR/decode.json"

  jq -e '.status == "executed" and .output_tensor.bytes > 0' "$OUT_DIR/decode.json" >/dev/null
  test -s "$OUT_DIR/out.f32"

  jq -s '{prepare: .[0], decode: .[1], evidence_dir: "'"$OUT_DIR"'"}' \
    "$OUT_DIR/prepare.json" \
    "$OUT_DIR/decode.json" \
    > "$OUT_DIR/summary.json"

  echo "PASS: llama.cpp stage decode bridge smoke"
  echo "Evidence: $OUT_DIR"
}

main "$@"
