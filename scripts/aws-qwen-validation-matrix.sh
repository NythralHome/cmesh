#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_ID="${CMESH_RUN_ID:-cmesh-qwen-validation-$(date -u +%Y%m%d%H%M%S)}"
EVIDENCE_DIR="${CMESH_EVIDENCE_DIR:-/tmp/$RUN_ID}"
MODELS="${CMESH_QWEN_VALIDATION_MODELS:-qwen2.5-1.5b-instruct-q4-k-m qwen2.5-3b-instruct-q4-k-m}"
PROMPT="${CMESH_QWEN_VALIDATION_PROMPT:-You are running inside CMesh. Answer in one concise Ukrainian sentence: what is CMesh testing right now?}"
MAX_TOKENS="${CMESH_QWEN_VALIDATION_MAX_TOKENS:-3}"

mkdir -p "$EVIDENCE_DIR"

model_repo() {
  case "$1" in
    qwen2.5-0.5b-instruct-q4-k-m) echo "Qwen/Qwen2.5-0.5B-Instruct-GGUF" ;;
    qwen2.5-1.5b-instruct-q4-k-m) echo "Qwen/Qwen2.5-1.5B-Instruct-GGUF" ;;
    qwen2.5-3b-instruct-q4-k-m) echo "Qwen/Qwen2.5-3B-Instruct-GGUF" ;;
    qwen2.5-7b-instruct-q4-k-m) echo "bartowski/Qwen2.5-7B-Instruct-GGUF" ;;
    qwen2.5-coder-7b-instruct-q4-k-m) echo "Qwen/Qwen2.5-Coder-7B-Instruct-GGUF" ;;
    qwen2.5-14b-instruct-q4-k-m) echo "bartowski/Qwen2.5-14B-Instruct-GGUF" ;;
    qwen2.5-32b-instruct-q4-k-m) echo "bartowski/Qwen2.5-32B-Instruct-GGUF" ;;
    qwen2.5-coder-32b-instruct-q4-k-m) echo "bartowski/Qwen2.5-Coder-32B-Instruct-GGUF" ;;
    deepseek-r1-distill-qwen-32b-q4-k-m) echo "bartowski/DeepSeek-R1-Distill-Qwen-32B-GGUF" ;;
    *) return 1 ;;
  esac
}

model_file() {
  case "$1" in
    qwen2.5-0.5b-instruct-q4-k-m) echo "qwen2.5-0.5b-instruct-q4_k_m.gguf" ;;
    qwen2.5-1.5b-instruct-q4-k-m) echo "qwen2.5-1.5b-instruct-q4_k_m.gguf" ;;
    qwen2.5-3b-instruct-q4-k-m) echo "qwen2.5-3b-instruct-q4_k_m.gguf" ;;
    qwen2.5-7b-instruct-q4-k-m) echo "Qwen2.5-7B-Instruct-Q4_K_M.gguf" ;;
    qwen2.5-coder-7b-instruct-q4-k-m) echo "qwen2.5-coder-7b-instruct-q4_k_m.gguf" ;;
    qwen2.5-14b-instruct-q4-k-m) echo "Qwen2.5-14B-Instruct-Q4_K_M.gguf" ;;
    qwen2.5-32b-instruct-q4-k-m) echo "Qwen2.5-32B-Instruct-Q4_K_M.gguf" ;;
    qwen2.5-coder-32b-instruct-q4-k-m) echo "Qwen2.5-Coder-32B-Instruct-Q4_K_M.gguf" ;;
    deepseek-r1-distill-qwen-32b-q4-k-m) echo "DeepSeek-R1-Distill-Qwen-32B-Q4_K_M.gguf" ;;
    *) return 1 ;;
  esac
}

model_layers() {
  case "$1" in
    qwen2.5-0.5b-instruct-q4-k-m) echo "24" ;;
    qwen2.5-1.5b-instruct-q4-k-m) echo "28" ;;
    qwen2.5-3b-instruct-q4-k-m) echo "36" ;;
    qwen2.5-7b-instruct-q4-k-m|qwen2.5-coder-7b-instruct-q4-k-m) echo "28" ;;
    qwen2.5-14b-instruct-q4-k-m) echo "48" ;;
    *) echo "64" ;;
  esac
}

summary_json="$EVIDENCE_DIR/summary.jsonl"
summary_md="$EVIDENCE_DIR/comparison.md"
: >"$summary_json"

{
  echo "# Qwen Single vs Distributed Validation"
  echo
  echo "- Run ID: \`$RUN_ID\`"
  echo "- Prompt: \`$PROMPT\`"
  echo "- Max tokens: \`$MAX_TOKENS\`"
  echo
  echo "| Model | Single ms | Distributed ms | Dispatch ms | Receive wait ms | Relay write ms | Stage compute ms | Single response | Distributed response | Evidence |"
  echo "|---|---:|---:|---:|---:|---:|---:|---|---|---|"
} >"$summary_md"

for model_id in $MODELS; do
  repo="$(model_repo "$model_id")"
  file="$(model_file "$model_id")"
  layers="$(model_layers "$model_id")"
  model_run_id="$RUN_ID-$model_id"
  model_evidence="$EVIDENCE_DIR/$model_id"
  mkdir -p "$model_evidence"

  echo "==> validating $model_id"
  (
    export CMESH_RUN_ID="$model_run_id"
    export CMESH_E2E_DIR="$model_evidence"
    export CMESH_MODEL_ID="$model_id"
    export CMESH_MODEL_URL="https://huggingface.co/$repo/resolve/main/$file"
    export CMESH_MODEL_FILE="$file"
    export CMESH_EXPECTED_MODEL_LAYERS="$layers"
    export CMESH_PROMPT="$PROMPT"
    export CMESH_DISTRIBUTED_MAX_TOKENS="$MAX_TOKENS"
    export CMESH_SINGLE_MAX_TOKENS="$MAX_TOKENS"
    export CMESH_RUN_SINGLE_WORKER_REGRESSION=true
    export CMESH_ALLOW_SINGLE_WORKER_REGRESSION_FAILURE="${CMESH_ALLOW_SINGLE_WORKER_REGRESSION_FAILURE:-true}"
    "$ROOT_DIR/scripts/aws-4node-qwen-e2e.sh"
  )

  summary_file="$model_evidence/summary.json"
  if [[ ! -f "$summary_file" ]]; then
    echo "missing summary for $model_id at $summary_file" >&2
    exit 1
  fi

  jq -c --arg model_id "$model_id" --arg evidence "$model_evidence" \
    '. + {model_id: $model_id, evidence_dir: $evidence}' "$summary_file" >>"$summary_json"

  single_ms="$(jq -r '.single_elapsed_ms // "-"' "$summary_file")"
  distributed_ms="$(jq -r '.distributed_elapsed_ms // "-"' "$summary_file")"
  dispatch_ms="$(jq -r '.dispatch_elapsed_ms // "-"' "$summary_file")"
  receive_ms="$(jq -r '.activation_timing.receive_wait_ms_total // "-"' "$summary_file")"
  relay_ms="$(jq -r '.activation_timing.relay_write_ms_total // "-"' "$summary_file")"
  compute_ms="$(jq -r '.activation_timing.stage_compute_ms_total // "-"' "$summary_file")"
  single_response="$(jq -r '.single_output // "-"' "$summary_file" | tr '\n' ' ' | sed 's/|/\\|/g')"
  distributed_response="$(jq -r '.dispatch_output // .distributed_output // "-"' "$summary_file" | tr '\n' ' ' | sed 's/|/\\|/g')"
  echo "| \`$model_id\` | $single_ms | $distributed_ms | $dispatch_ms | $receive_ms | $relay_ms | $compute_ms | $single_response | $distributed_response | \`$model_evidence\` |" >>"$summary_md"
done

echo "evidence: $EVIDENCE_DIR"
echo "summary:  $summary_md"
