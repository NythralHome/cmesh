#!/usr/bin/env bash
set -euo pipefail

MODEL_PATH="${CMESH_GGUF_MODEL_PATH:-${1:-}}"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-llama-stage-prepare-smoke}"
OUT_DIR="${CMESH_LLAMA_STAGE_PREPARE_SMOKE_DIR:-/tmp/cmesh-llama-stage-prepare-smoke-$(date -u +%Y%m%d%H%M%S)}"
STAGE_START="${CMESH_STAGE_START:-0}"
STAGE_END="${CMESH_STAGE_END:-1}"
STAGE_INDEX="${CMESH_STAGE_INDEX:-0}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: $1 is required" >&2
    exit 1
  }
}

main() {
  need jq
  need bash

  if [ -z "$MODEL_PATH" ]; then
    echo "error: pass a GGUF path or set CMESH_GGUF_MODEL_PATH" >&2
    exit 2
  fi
  if [ ! -f "$MODEL_PATH" ]; then
    echo "error: model file does not exist: $MODEL_PATH" >&2
    exit 2
  fi

  mkdir -p "$OUT_DIR"

  WORK_DIR="$WORK_DIR" scripts/prepare-llamacpp-stage-runner-worktree.sh | tee "$OUT_DIR/build.log"
  runner="$WORK_DIR/src/build-cmesh-stage/bin/cmesh-stage-runner"
  if [ ! -x "$runner" ]; then
    echo "error: runner did not build: $runner" >&2
    exit 1
  fi

  "$runner" \
    --command prepare \
    --model "$MODEL_PATH" \
    --stage-start "$STAGE_START" \
    --stage-end "$STAGE_END" \
    --stage-index "$STAGE_INDEX" \
    --emit-tensor-list \
    --materialize-selected-tensors \
    | tee "$OUT_DIR/prepare.json" >/dev/null

  jq -e '
    .kind == "cmesh.llamacpp_stage_prepare" and
    .status == "metadata_ready" and
    .runtime == "llama.cpp" and
    .n_layer > 0 and
    .n_embd > 0 and
    .stage_layer_count > 0 and
    .expected_hidden_state.shape_template[2] == .n_embd and
    .tensor_manifest.source == "gguf metadata" and
    .tensor_manifest.manifest_only == true and
    .tensor_manifest.total_tensor_count > 0 and
    .tensor_manifest.selected_tensor_count > 0 and
    .tensor_manifest.stage_tensor_count > 0 and
    .tensor_manifest.selected_bytes > 0 and
    (.tensor_manifest.sample | length) > 0 and
    (.tensor_manifest.tensors | length) == .tensor_manifest.selected_tensor_count and
    .materialization_probe.requested == true and
    .materialization_probe.attempted == true and
    .materialization_probe.loaded == true and
    .selected_tensor_materialization_ready == true and
    .executable == false and
    .guardrail == "metadata prepare only; not real layer sharding yet"
  ' "$OUT_DIR/prepare.json" >/dev/null

  jq '{model:.model_name,arch:.model_architecture,n_layer,n_embd,stage:{index:.stage_index,start:.stage_start,end:.stage_end,count:.stage_layer_count},hidden_state:.expected_hidden_state,tensor_manifest:.tensor_manifest,executable,guardrail}' "$OUT_DIR/prepare.json" | tee "$OUT_DIR/summary.json"
  echo "PASS: llama.cpp stage prepare metadata smoke succeeded"
  echo "Evidence: $OUT_DIR"
}

main "$@"
