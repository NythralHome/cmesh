#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_ID="${CMESH_READINESS_RUN_ID:-cmesh-production-readiness-$(date -u +%Y%m%d%H%M%S)}"
OUT_DIR="${CMESH_READINESS_DIR:-/tmp/$RUN_ID}"
RUN_AWS_E2E="${CMESH_RUN_AWS_E2E:-false}"
STAGE_RUNTIME_ARCHIVE="${CMESH_STAGE_RUNTIME_ARCHIVE:-$ROOT_DIR/dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz}"
REQUIRE_RESIDENT_PROTOCOL_STATIC="${CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC:-true}"
REPORT="$OUT_DIR/report.txt"

mkdir -p "$OUT_DIR"
: > "$REPORT"

log() {
  printf '%s\n' "$*" | tee -a "$REPORT"
}

run_step() {
  local name="$1"
  shift
  log ""
  log "==> $name"
  "$@" 2>&1 | tee "$OUT_DIR/${name//[^A-Za-z0-9_.-]/_}.log"
  log "ok: $name"
}

fail() {
  log "FAIL: $*"
  exit 1
}

main() {
  log "CMesh production readiness gate"
  log "run_id: $RUN_ID"
  log "out_dir: $OUT_DIR"
  log "aws_e2e: $RUN_AWS_E2E"
  log "stage_runtime_archive: $STAGE_RUNTIME_ARCHIVE"
  log "require_resident_protocol_static: $REQUIRE_RESIDENT_PROTOCOL_STATIC"

  run_step "prepare-current-stage-runtime-artifact" bash -lc "cd '$ROOT_DIR' && CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC='$REQUIRE_RESIDENT_PROTOCOL_STATIC' CMESH_STAGE_RUNTIME_ARCHIVE='${CMESH_STAGE_RUNTIME_ARCHIVE:-}' scripts/prepare-current-stage-runtime-artifact.sh"
  [[ -f "$STAGE_RUNTIME_ARCHIVE" ]] || fail "missing stage runtime archive after prepare: $STAGE_RUNTIME_ARCHIVE"

  run_step "go-test" bash -lc "cd '$ROOT_DIR' && go test ./..."
  run_step "shell-syntax" bash -lc "cd '$ROOT_DIR' && bash -n scripts/*.sh"
  run_step "diff-check" bash -lc "cd '$ROOT_DIR' && git diff --check"
  run_step "cdip-memory-aware-placement-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_CDIP_MEMORY_PLACEMENT_SMOKE_DIR='$OUT_DIR/cdip-memory-aware-placement-smoke' scripts/cdip-memory-aware-placement-smoke.sh"
  run_step "production-security-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_PRODUCTION_SECURITY_SMOKE_DIR='$OUT_DIR/production-security-smoke' scripts/production-security-smoke.sh"
  run_step "installers-dry-run-smoke" bash -lc "cd '$ROOT_DIR' && WORK_DIR='$OUT_DIR/installers-dry-run-smoke' scripts/installers-dry-run-smoke.sh"
  run_step "observability-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_OBSERVABILITY_SMOKE_DIR='$OUT_DIR/observability-smoke' scripts/observability-smoke.sh"
  run_step "cdip-resident-prepare-probe-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_CDIP_RESIDENT_PREPARE_PROBE_SMOKE_DIR='$OUT_DIR/cdip-resident-prepare-probe-smoke' scripts/cdip-resident-prepare-probe-smoke.sh"
  run_step "cdip-resident-runner-contract-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_CDIP_RESIDENT_RUNNER_CONTRACT_SMOKE_DIR='$OUT_DIR/cdip-resident-runner-contract-smoke' scripts/cdip-resident-runner-contract-smoke.sh"
  run_step "llamacpp-resident-loop-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_LLAMA_CPP_RESIDENT_LOOP_SMOKE_DIR='$OUT_DIR/llamacpp-resident-loop-smoke' scripts/llamacpp-resident-loop-smoke.sh"
  run_step "llamacpp-resident-native-prepare-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_LLAMA_CPP_RESIDENT_NATIVE_PREPARE_SMOKE_DIR='$OUT_DIR/llamacpp-resident-native-prepare-smoke' scripts/llamacpp-resident-native-prepare-smoke.sh"
  run_step "llamacpp-resident-source-decode-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_LLAMA_CPP_RESIDENT_SOURCE_DECODE_SMOKE_DIR='$OUT_DIR/llamacpp-resident-source-decode-smoke' scripts/llamacpp-resident-source-decode-smoke.sh"
  run_step "cdip-resident-native-prepare-daemon-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_CDIP_RESIDENT_NATIVE_PREPARE_DAEMON_SMOKE_DIR='$OUT_DIR/cdip-resident-native-prepare-daemon-smoke' scripts/cdip-resident-native-prepare-daemon-smoke.sh"
  run_step "cdip-daemon-decode-loop-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_CDIP_DAEMON_DECODE_LOOP_SMOKE_DIR='$OUT_DIR/cdip-daemon-decode-loop-smoke' scripts/cdip-daemon-decode-loop-smoke.sh"
  run_step "cdip-recovery-cleanup-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_CDIP_RECOVERY_CLEANUP_SMOKE_DIR='$OUT_DIR/cdip-recovery-cleanup-smoke' scripts/cdip-recovery-cleanup-smoke.sh"
  run_step "cdip-daemon-session-recreate-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_CDIP_DAEMON_SESSION_RECREATE_SMOKE_DIR='$OUT_DIR/cdip-daemon-session-recreate-smoke' scripts/cdip-daemon-session-recreate-smoke.sh"
  run_step "cdip-real-gguf-multi-daemon-worker-smoke" bash -lc "cd '$ROOT_DIR' && CMESH_DOWNLOAD_GGUF_FIXTURE='${CMESH_DOWNLOAD_GGUF_FIXTURE:-1}' CMESH_CDIP_REAL_GGUF_WORKER_SMOKE_DIR='$OUT_DIR/cdip-real-gguf-multi-daemon-worker-smoke' scripts/cdip-real-gguf-worker-execution-smoke.sh"
  run_step "runtime-artifact-verify" bash -lc "CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC='$REQUIRE_RESIDENT_PROTOCOL_STATIC' '$ROOT_DIR/scripts/verify-llamacpp-runtime-artifact.sh' '$STAGE_RUNTIME_ARCHIVE'"
  run_step "aws-cdip-preflight" bash -lc "cd '$ROOT_DIR' && CMESH_MODEL_ID='qwen2.5-14b-instruct-q4-k-m' CMESH_MODEL_URL='https://huggingface.co/bartowski/Qwen2.5-14B-Instruct-GGUF/resolve/main/Qwen2.5-14B-Instruct-Q4_K_M.gguf' CMESH_MODEL_FILE='Qwen2.5-14B-Instruct-Q4_K_M.gguf' CMESH_EXPECTED_MODEL_LAYERS=48 CMESH_PLACEMENT_PROOF_MODEL_ID='qwen2.5-14b-instruct-q4-k-m' CMESH_REQUIRE_MEMORY_PRESSURE_EXECUTION=true CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC='$REQUIRE_RESIDENT_PROTOCOL_STATIC' CMESH_PREFLIGHT_ONLY=true CMESH_REQUIRE_STAGE_RUNTIME_ARTIFACT=true CMESH_E2E_DIR='$OUT_DIR/aws-cdip-preflight' CMESH_STAGE_RUNTIME_ARCHIVE='$STAGE_RUNTIME_ARCHIVE' scripts/aws-cdip-real-gguf-e2e.sh"

  if [[ "$RUN_AWS_E2E" == "true" ]]; then
    run_step "aws-installers-e2e" bash -lc "cd '$ROOT_DIR' && CMESH_E2E_DIR='$OUT_DIR/aws-installers-e2e' scripts/aws-installers-e2e.sh"
    run_step "aws-cdip-real-gguf-e2e" bash -lc "cd '$ROOT_DIR' && CMESH_MODEL_ID='qwen2.5-14b-instruct-q4-k-m' CMESH_MODEL_URL='https://huggingface.co/bartowski/Qwen2.5-14B-Instruct-GGUF/resolve/main/Qwen2.5-14B-Instruct-Q4_K_M.gguf' CMESH_MODEL_FILE='Qwen2.5-14B-Instruct-Q4_K_M.gguf' CMESH_EXPECTED_MODEL_LAYERS=48 CMESH_PLACEMENT_PROOF_MODEL_ID='qwen2.5-14b-instruct-q4-k-m' CMESH_REQUIRE_MEMORY_PRESSURE_EXECUTION=true CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC='$REQUIRE_RESIDENT_PROTOCOL_STATIC' CMESH_E2E_DIR='$OUT_DIR/aws-cdip-real-gguf-e2e' CMESH_AWS_INSTANCE_COUNT=3 CMESH_AWS_INSTANCE_TYPE=t3.large CMESH_AWS_VOLUME_SIZE=80 CMESH_REQUIRE_STAGE_RUNTIME_ARTIFACT=true CMESH_INSTALL_MANAGER_SERVICE=true CMESH_INSTALL_STAGE_WORKER_SERVICES=true CMESH_STAGE_RUNTIME_ARCHIVE='$STAGE_RUNTIME_ARCHIVE' scripts/aws-cdip-real-gguf-e2e.sh"
  else
    log ""
    log "skipped: AWS E2E disabled; set CMESH_RUN_AWS_E2E=true to create temporary EC2 instances"
  fi

  log ""
  log "PASS: CMesh production readiness gate completed"
  log "evidence: $OUT_DIR"
}

main "$@"
