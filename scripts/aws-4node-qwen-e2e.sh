#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

RUN_ID="${CMESH_RUN_ID:-cmesh-4node-qwen-e2e-$(date -u +%Y%m%d%H%M%S)}"
PACKAGE_DIR="${CMESH_LINUX_PACKAGE_DIR:-}"

if [[ -z "$PACKAGE_DIR" ]]; then
  latest_release_dir="$(ls -dt "$ROOT_DIR"/dist/linux-production/v0.1.0-linux-rc.1 2>/dev/null | head -1 || true)"
  if [[ -n "$latest_release_dir" ]]; then
    PACKAGE_DIR="$latest_release_dir"
  fi
fi

if [[ -z "$PACKAGE_DIR" ]]; then
  echo "error: set CMESH_LINUX_PACKAGE_DIR to an unpacked Linux release package directory" >&2
  exit 1
fi

export CMESH_RUN_ID="$RUN_ID"
export CMESH_E2E_DIR="${CMESH_E2E_DIR:-/tmp/$RUN_ID}"
export CMESH_LINUX_PACKAGE_DIR="$PACKAGE_DIR"
export CMESH_AWS_INSTANCE_COUNT="${CMESH_AWS_INSTANCE_COUNT:-4}"
export CMESH_MANAGER_AS_STAGE_WORKER=false
export CMESH_INSTALL_MANAGER_SERVICE=true
export CMESH_INSTALL_STAGE_WORKER_SERVICES=true
export CMESH_REQUIRE_STAGE_RUNTIME_ARTIFACT=true
export CMESH_RUN_SINGLE_WORKER_REGRESSION=true
export CMESH_STAGE_WORKER_MEMORY_GB="${CMESH_STAGE_WORKER_MEMORY_GB:-8}"
export CMESH_REQUIRE_MEMORY_PRESSURE_EXECUTION="${CMESH_REQUIRE_MEMORY_PRESSURE_EXECUTION:-false}"
export CMESH_MODEL_ID="${CMESH_MODEL_ID:-qwen2.5-0.5b-instruct-q4-k-m}"
export CMESH_MODEL_URL="${CMESH_MODEL_URL:-https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/qwen2.5-0.5b-instruct-q4_k_m.gguf}"
export CMESH_MODEL_FILE="${CMESH_MODEL_FILE:-qwen2.5-0.5b-instruct-q4_k_m.gguf}"
export CMESH_EXPECTED_MODEL_LAYERS="${CMESH_EXPECTED_MODEL_LAYERS:-24}"
export CMESH_PLACEMENT_PROOF_MODEL_ID="${CMESH_PLACEMENT_PROOF_MODEL_ID:-qwen2.5-14b-instruct-q4-k-m}"
export CMESH_PROMPT="${CMESH_PROMPT:-You are running inside CMesh. Answer in one concise Ukrainian sentence: what is CMesh testing right now?}"

exec "$ROOT_DIR/scripts/aws-cdip-real-gguf-e2e.sh"
