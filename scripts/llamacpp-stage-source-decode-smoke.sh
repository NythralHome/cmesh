#!/usr/bin/env bash
set -euo pipefail

if [[ -z "${CMESH_GGUF_MODEL_PATH:-}" ]]; then
  echo "SKIP: set CMESH_GGUF_MODEL_PATH=/path/to/qwen.gguf to run the source decode bridge smoke"
  exit 0
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR="${WORK_DIR:-/tmp/cmesh-llama-stage-source-decode-smoke}"
RUN_DIR="${CMESH_STAGE_SOURCE_SMOKE_DIR:-/tmp/cmesh-stage-source-decode-smoke-$(date -u +%Y%m%d%H%M%S)}"
PROMPT="${CMESH_STAGE_SOURCE_PROMPT:-hello from cmesh source stage}"
STAGE_START="${CMESH_STAGE_START:-0}"
STAGE_END="${CMESH_STAGE_END:-1}"

mkdir -p "$RUN_DIR"

WORK_DIR="$WORK_DIR" "$ROOT_DIR/scripts/prepare-llamacpp-stage-runner-worktree.sh" >/dev/null
RUNNER="$WORK_DIR/src/build-cmesh-stage/bin/cmesh-stage-runner"

"$RUNNER" \
  --command source-decode \
  --model "$CMESH_GGUF_MODEL_PATH" \
  --stage-start "$STAGE_START" \
  --stage-end "$STAGE_END" \
  --stage-index 0 \
  --prompt "$PROMPT" \
  --output-file "$RUN_DIR/source-activation.bin" \
  > "$RUN_DIR/source-decode.json"

jq -e '
  .kind == "cmesh.llamacpp_stage_source_decode" and
  .status == "executed" and
  .output_tensor.dtype == "f32" and
  .output_tensor.bytes > 0
' "$RUN_DIR/source-decode.json" >/dev/null

test -s "$RUN_DIR/source-activation.bin"

echo "PASS: llama.cpp source decode bridge smoke succeeded"
echo "Evidence: $RUN_DIR"
