#!/usr/bin/env bash
set -euo pipefail

MODEL_PATH="${CMESH_GGUF_MODEL_PATH:-${1:-}}"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-llama-stage-shard-bundle-smoke}"
OUT_DIR="${CMESH_LLAMA_STAGE_SHARD_BUNDLE_SMOKE_DIR:-/tmp/cmesh-llama-stage-shard-bundle-smoke-$(date -u +%Y%m%d%H%M%S)}"
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

  if [[ -z "$MODEL_PATH" ]]; then
    MODEL_PATH="$(CMESH_DOWNLOAD_GGUF_FIXTURE=1 scripts/ensure-gguf-fixture.sh)"
  fi
  if [[ ! -f "$MODEL_PATH" ]]; then
    echo "error: model file does not exist: $MODEL_PATH" >&2
    exit 2
  fi

  mkdir -p "$OUT_DIR"

  WORK_DIR="$WORK_DIR" scripts/prepare-llamacpp-stage-runner-worktree.sh > "$OUT_DIR/build.log"
  runner="$WORK_DIR/src/build-cmesh-stage/bin/cmesh-stage-runner"
  if [[ ! -x "$runner" ]]; then
    echo "error: runner did not build: $runner" >&2
    exit 1
  fi

  bundle="$OUT_DIR/stage-$STAGE_INDEX.cmesh-shard"
  "$runner" \
    --command write-shard-bundle \
    --model "$MODEL_PATH" \
    --stage-start "$STAGE_START" \
    --stage-end "$STAGE_END" \
    --stage-index "$STAGE_INDEX" \
    --first-stage \
    --output-file "$bundle" \
    | tee "$OUT_DIR/write-shard-bundle.json" >/dev/null

  jq -e --arg bundle "$bundle" '
    .kind == "cmesh.llamacpp_stage_shard_bundle" and
    .status == "bundle_ready_not_loadable_gguf" and
    .protocol == "cdip.cmesh-shard-bundle-v1" and
    .output_file == $bundle and
    .selected_tensor_count > 0 and
    .selected_bytes > 0 and
    .bundle_bytes > .selected_bytes and
    .loadable_gguf == false
  ' "$OUT_DIR/write-shard-bundle.json" >/dev/null

  if [[ ! -s "$bundle" ]]; then
    echo "error: shard bundle was not written: $bundle" >&2
    exit 1
  fi
  head -c 22 "$bundle" | grep -q "CMESH_SHARD_BUNDLE_V1" || {
    echo "error: shard bundle magic header missing" >&2
    exit 1
  }

  "$runner" \
    --command inspect-shard-bundle \
    --bundle-file "$bundle" \
    | tee "$OUT_DIR/inspect-shard-bundle.json" >/dev/null

  jq -e --arg bundle "$bundle" --slurpfile write "$OUT_DIR/write-shard-bundle.json" '
    .kind == "cmesh.llamacpp_stage_shard_bundle_inspect" and
    .status == "bundle_valid" and
    .protocol == "cdip.cmesh-shard-bundle-v1" and
    .bundle_file == $bundle and
    .stage_index == $write[0].stage_index and
    .stage_start == $write[0].stage_start and
    .stage_end == $write[0].stage_end and
    .selected_tensor_count == $write[0].selected_tensor_count and
    .selected_bytes == $write[0].selected_bytes and
    .payload_bytes == $write[0].selected_bytes and
    .bundle_bytes == $write[0].bundle_bytes and
    (.first_tensor_name | length) > 0 and
    .first_tensor_bytes > 0 and
    .loadable_gguf == false
  ' "$OUT_DIR/inspect-shard-bundle.json" >/dev/null

  first_tensor_name="$(jq -r '.first_tensor_name' "$OUT_DIR/inspect-shard-bundle.json")"
  first_tensor_bytes="$(jq -r '.first_tensor_bytes' "$OUT_DIR/inspect-shard-bundle.json")"
  tensor_file="$OUT_DIR/first-tensor.bin"
  "$runner" \
    --command extract-shard-tensor \
    --bundle-file "$bundle" \
    --tensor-name "$first_tensor_name" \
    --output-file "$tensor_file" \
    | tee "$OUT_DIR/extract-shard-tensor.json" >/dev/null

  jq -e --arg tensor "$first_tensor_name" --arg output "$tensor_file" --argjson bytes "$first_tensor_bytes" '
    .kind == "cmesh.llamacpp_stage_shard_tensor_extract" and
    .status == "tensor_extracted" and
    .protocol == "cdip.cmesh-shard-bundle-v1" and
    .tensor_name == $tensor and
    .output_file == $output and
    .tensor_bytes == $bytes and
    .output_bytes == $bytes and
    .loadable_gguf == false
  ' "$OUT_DIR/extract-shard-tensor.json" >/dev/null
  [[ "$(wc -c < "$tensor_file" | tr -d ' ')" == "$first_tensor_bytes" ]] || {
    echo "error: extracted tensor byte count mismatch" >&2
    exit 1
  }

  "$runner" \
    --command verify-shard-tensor-source \
    --bundle-file "$bundle" \
    --model "$MODEL_PATH" \
    --tensor-name "$first_tensor_name" \
    | tee "$OUT_DIR/verify-shard-tensor-source.json" >/dev/null

  jq -e --arg tensor "$first_tensor_name" --argjson bytes "$first_tensor_bytes" '
    .kind == "cmesh.llamacpp_stage_shard_tensor_verify" and
    .status == "tensor_source_match" and
    .protocol == "cdip.cmesh-shard-bundle-v1" and
    .tensor_name == $tensor and
    .tensor_bytes == $bytes and
    .compared_bytes == $bytes and
    .bytes_match == true and
    .loadable_gguf == false
  ' "$OUT_DIR/verify-shard-tensor-source.json" >/dev/null

  "$runner" \
    --command verify-shard-bundle-source \
    --bundle-file "$bundle" \
    --model "$MODEL_PATH" \
    | tee "$OUT_DIR/verify-shard-bundle-source.json" >/dev/null

  jq -e --slurpfile write "$OUT_DIR/write-shard-bundle.json" '
    .kind == "cmesh.llamacpp_stage_shard_bundle_verify" and
    .status == "bundle_source_match" and
    .protocol == "cdip.cmesh-shard-bundle-v1" and
    .verified_tensor_count == $write[0].selected_tensor_count and
    .verified_bytes == $write[0].selected_bytes and
    .selected_tensor_count == $write[0].selected_tensor_count and
    .selected_bytes == $write[0].selected_bytes and
    .bytes_match == true and
    .loadable_gguf == false
  ' "$OUT_DIR/verify-shard-bundle-source.json" >/dev/null

  stage_gguf="$OUT_DIR/stage-$STAGE_INDEX.gguf"
  "$runner" \
    --command write-stage-gguf-shard \
    --bundle-file "$bundle" \
    --model "$MODEL_PATH" \
    --output-file "$stage_gguf" \
    | tee "$OUT_DIR/write-stage-gguf-shard.json" >/dev/null

  jq -e --arg output "$stage_gguf" --slurpfile write "$OUT_DIR/write-shard-bundle.json" '
    .kind == "cmesh.llamacpp_stage_gguf_shard" and
    .status == "stage_gguf_shard_ready_not_full_model" and
    .protocol == "cdip.cmesh-stage-gguf-shard-v1" and
    .output_file == $output and
    .selected_tensor_count == $write[0].selected_tensor_count and
    .selected_bytes == $write[0].selected_bytes and
    .written_tensor_count == $write[0].selected_tensor_count and
    .reopened_tensor_count == $write[0].selected_tensor_count and
    .shard_bytes > .selected_bytes and
    .loadable_full_model == false
  ' "$OUT_DIR/write-stage-gguf-shard.json" >/dev/null
  [[ -s "$stage_gguf" ]] || {
    echo "error: stage GGUF shard was not written" >&2
    exit 1
  }

  "$runner" \
    --command probe-stage-gguf-load \
    --model "$stage_gguf" \
    | tee "$OUT_DIR/probe-stage-gguf-load.json" >/dev/null

  jq -e --arg model "$stage_gguf" --slurpfile write "$OUT_DIR/write-shard-bundle.json" '
    .kind == "cmesh.llamacpp_stage_gguf_load_probe" and
    .status == "stage_model_loaded_partial" and
    .model_path == $model and
    .loaded == true and
    .cmesh_stage_metadata == true and
    .stage_start == $write[0].stage_start and
    .stage_end == $write[0].stage_end and
    .selected_tensor_count == $write[0].selected_tensor_count and
    .allowlist_tensor_count == $write[0].selected_tensor_count and
    .loadable_full_model == false and
    (.guardrail | contains("stage activation IO validation"))
  ' "$OUT_DIR/probe-stage-gguf-load.json" >/dev/null

  stage_activation="$OUT_DIR/stage-$STAGE_INDEX-source.bin"
  "$runner" \
    --command source-decode \
    --model "$stage_gguf" \
    --stage-start "$STAGE_START" \
    --stage-end "$STAGE_END" \
    --stage-index "$STAGE_INDEX" \
    --prompt "hello from cmesh" \
    --output-file "$stage_activation" \
    | tee "$OUT_DIR/source-decode-stage-gguf.json" >/dev/null

  jq -e --arg model "$stage_gguf" --arg output "$stage_activation" --slurpfile write "$OUT_DIR/write-shard-bundle.json" '
    .kind == "cmesh.llamacpp_stage_source_decode" and
    .status == "executed" and
    .model_path == $model and
    .stage_start == $write[0].stage_start and
    .stage_end == $write[0].stage_end and
    .output_tensor.path == $output and
    .output_tensor.bytes > 0 and
    .selected_tensor_count == $write[0].selected_tensor_count
  ' "$OUT_DIR/source-decode-stage-gguf.json" >/dev/null
  [[ -s "$stage_activation" ]] || {
    echo "error: stage GGUF source-decode did not write activation" >&2
    exit 1
  }

  jq --slurpfile inspect "$OUT_DIR/inspect-shard-bundle.json" --slurpfile extract "$OUT_DIR/extract-shard-tensor.json" --slurpfile verify "$OUT_DIR/verify-shard-tensor-source.json" --slurpfile bundle_verify "$OUT_DIR/verify-shard-bundle-source.json" --slurpfile gguf_shard "$OUT_DIR/write-stage-gguf-shard.json" --slurpfile load_probe "$OUT_DIR/probe-stage-gguf-load.json" --slurpfile source_decode "$OUT_DIR/source-decode-stage-gguf.json" '{status,protocol,stage:{index:.stage_index,start:.stage_start,end:.stage_end},selected_tensor_count,selected_bytes,bundle_bytes,inspect_status:$inspect[0].status,payload_bytes:$inspect[0].payload_bytes,extracted_tensor:$extract[0].tensor_name,extracted_tensor_bytes:$extract[0].tensor_bytes,source_verify_status:$verify[0].status,source_bytes_match:$verify[0].bytes_match,bundle_verify_status:$bundle_verify[0].status,bundle_verified_tensors:$bundle_verify[0].verified_tensor_count,bundle_verified_bytes:$bundle_verify[0].verified_bytes,stage_gguf_status:$gguf_shard[0].status,stage_gguf_bytes:$gguf_shard[0].shard_bytes,stage_gguf_tensors:$gguf_shard[0].written_tensor_count,stage_gguf_load_status:$load_probe[0].status,stage_gguf_loaded:$load_probe[0].loaded,stage_gguf_source_decode_status:$source_decode[0].status,stage_gguf_source_activation_bytes:$source_decode[0].output_tensor.bytes,loadable_gguf,guardrail}' "$OUT_DIR/write-shard-bundle.json" | tee "$OUT_DIR/summary.json"
  echo "PASS: llama.cpp stage shard bundle smoke succeeded"
  echo "Evidence: $OUT_DIR"
}

main "$@"
