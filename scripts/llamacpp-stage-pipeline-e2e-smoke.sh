#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! MODEL_PATH="$("$ROOT_DIR/scripts/ensure-gguf-fixture.sh")"; then
  echo "SKIP: set CMESH_GGUF_MODEL_PATH=/path/to/qwen.gguf or CMESH_DOWNLOAD_GGUF_FIXTURE=1 to run the real llama.cpp stage pipeline smoke"
  exit 0
fi
WORK_DIR="${WORK_DIR:-/tmp/cmesh-llama-stage-pipeline-e2e}"
RUN_DIR="${CMESH_STAGE_PIPELINE_SMOKE_DIR:-/tmp/cmesh-stage-pipeline-e2e-smoke-$(date -u +%Y%m%d%H%M%S)}"
PROMPT="${CMESH_STAGE_PIPELINE_PROMPT:-hello from cmesh pipeline}"

mkdir -p "$RUN_DIR"

WORK_DIR="$WORK_DIR" "$ROOT_DIR/scripts/prepare-llamacpp-stage-runner-worktree.sh" >/dev/null
RUNNER="$WORK_DIR/src/build-cmesh-stage/bin/cmesh-stage-runner"

"$RUNNER" \
  --command prepare \
  --model "$MODEL_PATH" \
  --stage-start 0 \
  --stage-end 0 \
  --stage-index 0 \
  > "$RUN_DIR/prepare.json"

N_LAYER="$(jq -r '.n_layer' "$RUN_DIR/prepare.json")"
N_EMBD="$(jq -r '.n_embd' "$RUN_DIR/prepare.json")"
if [[ -z "$N_LAYER" || "$N_LAYER" == "null" || "$N_LAYER" -lt 3 ]]; then
  echo "FAIL: expected model with at least 3 layers, got n_layer=$N_LAYER" >&2
  exit 1
fi

FIRST_END=$((N_LAYER / 3 - 1))
if [[ "$FIRST_END" -lt 0 ]]; then FIRST_END=0; fi
MIDDLE_START=$((FIRST_END + 1))
MIDDLE_END=$((2 * N_LAYER / 3 - 1))
if [[ "$MIDDLE_END" -lt "$MIDDLE_START" ]]; then MIDDLE_END="$MIDDLE_START"; fi
TERMINAL_START=$((MIDDLE_END + 1))
if [[ "$TERMINAL_START" -ge "$N_LAYER" ]]; then TERMINAL_START=$((N_LAYER - 1)); fi
TERMINAL_END=$((N_LAYER - 1))

"$RUNNER" \
  --command source-decode \
  --model "$MODEL_PATH" \
  --stage-start 0 \
  --stage-end "$FIRST_END" \
  --stage-index 0 \
  --prompt "$PROMPT" \
  --output-file "$RUN_DIR/source-activation.bin" \
  > "$RUN_DIR/source-decode.json"

SOURCE_SHAPE="$(jq -r '.output_tensor.shape | join(",")' "$RUN_DIR/source-decode.json")"
jq -e '.kind == "cmesh.llamacpp_stage_source_decode" and .status == "executed" and .output_tensor.dtype == "f32" and .output_tensor.bytes > 0' "$RUN_DIR/source-decode.json" >/dev/null
test -s "$RUN_DIR/source-activation.bin"

"$RUNNER" \
  --command decode \
  --model "$MODEL_PATH" \
  --stage-start "$MIDDLE_START" \
  --stage-end "$MIDDLE_END" \
  --stage-index 1 \
  --activation-file "$RUN_DIR/source-activation.bin" \
  --dtype f32 \
  --shape "$SOURCE_SHAPE" \
  --output-file "$RUN_DIR/relay-activation.bin" \
  > "$RUN_DIR/relay-decode.json"

RELAY_SHAPE="$(jq -r '.output_tensor.shape | join(",")' "$RUN_DIR/relay-decode.json")"
jq -e '.kind == "cmesh.llamacpp_stage_decode" and .status == "executed" and .output_tensor.dtype == "f32" and .output_tensor.bytes > 0' "$RUN_DIR/relay-decode.json" >/dev/null
test -s "$RUN_DIR/relay-activation.bin"

"$RUNNER" \
  --command terminal-decode \
  --model "$MODEL_PATH" \
  --stage-start "$TERMINAL_START" \
  --stage-end "$TERMINAL_END" \
  --stage-index 2 \
  --activation-file "$RUN_DIR/relay-activation.bin" \
  --dtype f32 \
  --shape "$RELAY_SHAPE" \
  > "$RUN_DIR/terminal-decode.json"

jq -e '.kind == "cmesh.llamacpp_stage_terminal_decode" and .status == "executed" and .terminal_stage == true and (.tokens | length == 1) and .final == true' "$RUN_DIR/terminal-decode.json" >/dev/null

cat > "$RUN_DIR/summary.json" <<JSON
{
  "model": "$MODEL_PATH",
  "n_layer": $N_LAYER,
  "n_embd": $N_EMBD,
  "stages": [
    {"index": 0, "start": 0, "end": $FIRST_END},
    {"index": 1, "start": $MIDDLE_START, "end": $MIDDLE_END},
    {"index": 2, "start": $TERMINAL_START, "end": $TERMINAL_END}
  ],
  "next_token_id": $(jq -r '.next_token_id' "$RUN_DIR/terminal-decode.json"),
  "next_token_text": $(jq -r '.next_token_text | @json' "$RUN_DIR/terminal-decode.json")
}
JSON

echo "PASS: llama.cpp real stage pipeline smoke succeeded"
echo "Evidence: $RUN_DIR"
