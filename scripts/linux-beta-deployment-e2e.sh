#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

CMESH_LINUX_PACKAGE_DIR="${CMESH_LINUX_PACKAGE_DIR:-}"
CMESH_BETA_E2E_DIR="${CMESH_BETA_E2E_DIR:-/tmp/cmesh-linux-beta-deployment-$(date -u +%Y%m%d%H%M%S)}"
CMESH_AWS_INSTANCE_TYPE="${CMESH_AWS_INSTANCE_TYPE:-t3.large}"
CMESH_AWS_VOLUME_SIZE="${CMESH_AWS_VOLUME_SIZE:-80}"
CMESH_MODEL_ID="${CMESH_MODEL_ID:-qwen2.5-14b-instruct-q4-k-m}"
CMESH_MODEL_URL="${CMESH_MODEL_URL:-https://huggingface.co/bartowski/Qwen2.5-14B-Instruct-GGUF/resolve/main/Qwen2.5-14B-Instruct-Q4_K_M.gguf}"
CMESH_MODEL_FILE="${CMESH_MODEL_FILE:-Qwen2.5-14B-Instruct-Q4_K_M.gguf}"
CMESH_EXPECTED_MODEL_LAYERS="${CMESH_EXPECTED_MODEL_LAYERS:-48}"
CMESH_PROMPT="${CMESH_PROMPT:-CMesh Linux beta sliced model proof}"

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

require_package() {
  [[ -n "$CMESH_LINUX_PACKAGE_DIR" ]] || fail "CMESH_LINUX_PACKAGE_DIR is required"
  CMESH_LINUX_PACKAGE_DIR="$(cd "$CMESH_LINUX_PACKAGE_DIR" && pwd -P)"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/manifest.json" ]] || fail "package missing manifest.json: $CMESH_LINUX_PACKAGE_DIR"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/checksums.txt" ]] || fail "package missing checksums.txt: $CMESH_LINUX_PACKAGE_DIR"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/cmesh-linux-amd64" ]] || fail "package missing cmesh-linux-amd64: $CMESH_LINUX_PACKAGE_DIR"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/install-manager-linux.sh" ]] || fail "package missing install-manager-linux.sh: $CMESH_LINUX_PACKAGE_DIR"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/install-worker.sh" ]] || fail "package missing install-worker.sh: $CMESH_LINUX_PACKAGE_DIR"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz" ]] || fail "package missing stage runtime archive: $CMESH_LINUX_PACKAGE_DIR"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz.sha256" ]] || fail "package missing stage runtime checksum: $CMESH_LINUX_PACKAGE_DIR"
  (cd "$CMESH_LINUX_PACKAGE_DIR" && shasum -a 256 -c checksums.txt >/dev/null)
  jq -e '.kind == "cmesh.linux.production.release.v1"' "$CMESH_LINUX_PACKAGE_DIR/manifest.json" >/dev/null
}

write_summary() {
  local status="$1"
  jq -n \
    --arg status "$status" \
    --arg package_dir "$CMESH_LINUX_PACKAGE_DIR" \
    --arg installer_evidence "$CMESH_BETA_E2E_DIR/installers" \
    --arg sliced_evidence "$CMESH_BETA_E2E_DIR/sliced" \
    --arg model_id "$CMESH_MODEL_ID" \
    --arg model_file "$CMESH_MODEL_FILE" \
    --arg instance_type "$CMESH_AWS_INSTANCE_TYPE" \
    --arg volume_size "$CMESH_AWS_VOLUME_SIZE" \
    '{
      kind: "cmesh.linux.beta_deployment_e2e",
      status: $status,
      package_dir: $package_dir,
      installer_evidence: $installer_evidence,
      sliced_evidence: $sliced_evidence,
      model: { id: $model_id, file: $model_file },
      aws: { instance_type: $instance_type, volume_size_gb: ($volume_size | tonumber) }
    }' > "$CMESH_BETA_E2E_DIR/summary.json"
}

main() {
  need jq
  need shasum
  need aws
  need ssh
  need scp
  need curl
  require_package

  mkdir -p "$CMESH_BETA_E2E_DIR"
  jq -n --arg package_dir "$CMESH_LINUX_PACKAGE_DIR" '{package_dir:$package_dir}' > "$CMESH_BETA_E2E_DIR/package.json"

  echo "running AWS installer E2E from Linux production package"
  env \
    CMESH_LINUX_PACKAGE_DIR="$CMESH_LINUX_PACKAGE_DIR" \
    CMESH_AWS_INSTANCE_TYPE="${CMESH_INSTALLER_AWS_INSTANCE_TYPE:-t3.small}" \
    CMESH_AWS_VOLUME_SIZE="${CMESH_INSTALLER_AWS_VOLUME_SIZE:-20}" \
    CMESH_AWS_INSTANCE_COUNT=3 \
    CMESH_E2E_DIR="$CMESH_BETA_E2E_DIR/installers" \
    "$ROOT_DIR/scripts/aws-installers-e2e.sh"

  echo "running AWS sliced GGUF E2E from Linux production package"
  env \
    CMESH_LINUX_PACKAGE_DIR="$CMESH_LINUX_PACKAGE_DIR" \
    CMESH_AWS_INSTANCE_TYPE="$CMESH_AWS_INSTANCE_TYPE" \
    CMESH_AWS_VOLUME_SIZE="$CMESH_AWS_VOLUME_SIZE" \
    CMESH_AWS_INSTANCE_COUNT=3 \
    CMESH_REQUIRE_STAGE_RUNTIME_ARTIFACT=true \
    CMESH_INSTALL_MANAGER_SERVICE=true \
    CMESH_INSTALL_STAGE_WORKER_SERVICES=true \
    CMESH_MANAGER_AS_STAGE_WORKER=true \
    CMESH_REQUIRE_MEMORY_PRESSURE_EXECUTION=true \
    CMESH_MODEL_ID="$CMESH_MODEL_ID" \
    CMESH_MODEL_URL="$CMESH_MODEL_URL" \
    CMESH_MODEL_FILE="$CMESH_MODEL_FILE" \
    CMESH_EXPECTED_MODEL_LAYERS="$CMESH_EXPECTED_MODEL_LAYERS" \
    CMESH_PLACEMENT_PROOF_MODEL_ID="$CMESH_MODEL_ID" \
    CMESH_PROMPT="$CMESH_PROMPT" \
    CMESH_E2E_DIR="$CMESH_BETA_E2E_DIR/sliced" \
    "$ROOT_DIR/scripts/aws-cdip-real-gguf-e2e.sh"

  write_summary "passed"
  jq . "$CMESH_BETA_E2E_DIR/summary.json"
}

main "$@"
