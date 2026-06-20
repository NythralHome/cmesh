#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if ! MODEL_PATH="$("$ROOT_DIR/scripts/ensure-gguf-fixture.sh")"; then
  echo "SKIP: set CMESH_GGUF_MODEL_PATH=/path/to/qwen.gguf or CMESH_DOWNLOAD_GGUF_FIXTURE=1 to run the real llama.cpp stage GGUF pipeline smoke"
  exit 0
fi

WORK_DIR="${WORK_DIR:-/tmp/cmesh-llama-stage-gguf-pipeline}"
RUN_DIR="${CMESH_STAGE_GGUF_PIPELINE_SMOKE_DIR:-/tmp/cmesh-stage-gguf-pipeline-smoke-$(date -u +%Y%m%d%H%M%S)}"
PROMPT="${CMESH_STAGE_PIPELINE_PROMPT:-hello from cmesh sliced gguf pipeline}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: $1 is required" >&2
    exit 1
  }
}

write_stage() {
  local index="$1"
  local start="$2"
  local end="$3"
  local first="$4"
  local terminal="$5"

  local bundle="$RUN_DIR/stage-$index.cmesh-shard"
  local gguf="$RUN_DIR/stage-$index.gguf"
  local args=(
    --command write-shard-bundle
    --model "$MODEL_PATH"
    --stage-start "$start"
    --stage-end "$end"
    --stage-index "$index"
    --output-file "$bundle"
  )
  if [[ "$first" == "true" ]]; then
    args+=(--first-stage)
  fi
  if [[ "$terminal" == "true" ]]; then
    args+=(--terminal-stage)
  fi

  "$RUNNER" "${args[@]}" > "$RUN_DIR/write-shard-bundle-$index.json"
  jq -e --argjson start "$start" --argjson end "$end" '
    .kind == "cmesh.llamacpp_stage_shard_bundle" and
    .status == "bundle_ready_not_loadable_gguf" and
    .stage_start == $start and
    .stage_end == $end and
    .selected_tensor_count > 0 and
    .selected_bytes > 0
  ' "$RUN_DIR/write-shard-bundle-$index.json" >/dev/null

  "$RUNNER" \
    --command verify-shard-bundle-source \
    --bundle-file "$bundle" \
    --model "$MODEL_PATH" \
    > "$RUN_DIR/verify-shard-bundle-source-$index.json"
  jq -e --slurpfile write "$RUN_DIR/write-shard-bundle-$index.json" '
    .status == "bundle_source_match" and
    .verified_tensor_count == $write[0].selected_tensor_count and
    .verified_bytes == $write[0].selected_bytes
  ' "$RUN_DIR/verify-shard-bundle-source-$index.json" >/dev/null

  "$RUNNER" \
    --command write-stage-gguf-shard \
    --bundle-file "$bundle" \
    --model "$MODEL_PATH" \
    --output-file "$gguf" \
    > "$RUN_DIR/write-stage-gguf-shard-$index.json"
  jq -e --arg output "$gguf" --slurpfile write "$RUN_DIR/write-shard-bundle-$index.json" '
    .kind == "cmesh.llamacpp_stage_gguf_shard" and
    .status == "stage_gguf_shard_ready_not_full_model" and
    .output_file == $output and
    .written_tensor_count == $write[0].selected_tensor_count and
    .reopened_tensor_count == $write[0].selected_tensor_count
  ' "$RUN_DIR/write-stage-gguf-shard-$index.json" >/dev/null

  "$RUNNER" \
    --command probe-stage-gguf-load \
    --model "$gguf" \
    > "$RUN_DIR/probe-stage-gguf-load-$index.json"
  jq -e --arg model "$gguf" --slurpfile write "$RUN_DIR/write-shard-bundle-$index.json" '
    .kind == "cmesh.llamacpp_stage_gguf_load_probe" and
    .status == "stage_model_loaded_partial" and
    .model_path == $model and
    .stage_start == $write[0].stage_start and
    .stage_end == $write[0].stage_end and
    .loaded == true
  ' "$RUN_DIR/probe-stage-gguf-load-$index.json" >/dev/null
}

main() {
  need jq

  mkdir -p "$RUN_DIR"
  WORK_DIR="$WORK_DIR" "$ROOT_DIR/scripts/prepare-llamacpp-stage-runner-worktree.sh" > "$RUN_DIR/build.log"
  RUNNER="$WORK_DIR/src/build-cmesh-stage/bin/cmesh-stage-runner"
  if [[ ! -x "$RUNNER" ]]; then
    echo "error: runner did not build: $RUNNER" >&2
    exit 1
  fi

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

  write_stage 0 0 "$FIRST_END" true false
  write_stage 1 "$MIDDLE_START" "$MIDDLE_END" false false
  write_stage 2 "$TERMINAL_START" "$TERMINAL_END" false true

  "$RUNNER" \
    --command source-decode \
    --model "$RUN_DIR/stage-0.gguf" \
    --stage-start 0 \
    --stage-end "$FIRST_END" \
    --stage-index 0 \
    --prompt "$PROMPT" \
    --output-file "$RUN_DIR/source-activation.bin" \
    > "$RUN_DIR/source-decode-stage-gguf.json"
  SOURCE_SHAPE="$(jq -r '.output_tensor.shape | join(",")' "$RUN_DIR/source-decode-stage-gguf.json")"
  jq -e '.kind == "cmesh.llamacpp_stage_source_decode" and .status == "executed" and .output_tensor.dtype == "f32" and .output_tensor.bytes > 0' "$RUN_DIR/source-decode-stage-gguf.json" >/dev/null
  test -s "$RUN_DIR/source-activation.bin"

  "$RUNNER" \
    --command decode \
    --model "$RUN_DIR/stage-1.gguf" \
    --stage-start "$MIDDLE_START" \
    --stage-end "$MIDDLE_END" \
    --stage-index 1 \
    --activation-file "$RUN_DIR/source-activation.bin" \
    --dtype f32 \
    --shape "$SOURCE_SHAPE" \
    --output-file "$RUN_DIR/relay-activation.bin" \
    > "$RUN_DIR/relay-decode-stage-gguf.json"
  RELAY_SHAPE="$(jq -r '.output_tensor.shape | join(",")' "$RUN_DIR/relay-decode-stage-gguf.json")"
  jq -e '.kind == "cmesh.llamacpp_stage_decode" and .status == "executed" and .output_tensor.dtype == "f32" and .output_tensor.bytes > 0' "$RUN_DIR/relay-decode-stage-gguf.json" >/dev/null
  test -s "$RUN_DIR/relay-activation.bin"

  "$RUNNER" \
    --command terminal-decode \
    --model "$RUN_DIR/stage-2.gguf" \
    --stage-start "$TERMINAL_START" \
    --stage-end "$TERMINAL_END" \
    --stage-index 2 \
    --activation-file "$RUN_DIR/relay-activation.bin" \
    --dtype f32 \
    --shape "$RELAY_SHAPE" \
    > "$RUN_DIR/terminal-decode-stage-gguf.json"
  jq -e '.kind == "cmesh.llamacpp_stage_terminal_decode" and .status == "executed" and .terminal_stage == true and (.tokens | length == 1) and .final == true' "$RUN_DIR/terminal-decode-stage-gguf.json" >/dev/null

  jq -n \
    --arg model "$MODEL_PATH" \
    --argjson n_layer "$N_LAYER" \
    --argjson n_embd "$N_EMBD" \
    --argjson first_end "$FIRST_END" \
    --argjson middle_start "$MIDDLE_START" \
    --argjson middle_end "$MIDDLE_END" \
    --argjson terminal_start "$TERMINAL_START" \
    --argjson terminal_end "$TERMINAL_END" \
    --slurpfile s0 "$RUN_DIR/write-stage-gguf-shard-0.json" \
    --slurpfile s1 "$RUN_DIR/write-stage-gguf-shard-1.json" \
    --slurpfile s2 "$RUN_DIR/write-stage-gguf-shard-2.json" \
    --slurpfile source "$RUN_DIR/source-decode-stage-gguf.json" \
    --slurpfile relay "$RUN_DIR/relay-decode-stage-gguf.json" \
    --slurpfile terminal "$RUN_DIR/terminal-decode-stage-gguf.json" \
    '{
      model: $model,
      status: "sliced_stage_gguf_pipeline_executed",
      n_layer: $n_layer,
      n_embd: $n_embd,
      stages: [
        {index: 0, start: 0, end: $first_end, gguf: $s0[0].output_file, bytes: $s0[0].shard_bytes},
        {index: 1, start: $middle_start, end: $middle_end, gguf: $s1[0].output_file, bytes: $s1[0].shard_bytes},
        {index: 2, start: $terminal_start, end: $terminal_end, gguf: $s2[0].output_file, bytes: $s2[0].shard_bytes}
      ],
      activation_bytes: {
        source: $source[0].output_tensor.bytes,
        relay: $relay[0].output_tensor.bytes
      },
      next_token_id: $terminal[0].next_token_id,
      next_token_text: $terminal[0].next_token_text
    }' > "$RUN_DIR/summary.json"

  echo "PASS: llama.cpp stage GGUF sliced pipeline smoke succeeded"
  echo "Evidence: $RUN_DIR"
}

main "$@"
