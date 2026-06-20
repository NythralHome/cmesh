#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-installers-dry-run-smoke-$(date -u +%Y%m%d%H%M%S)}"
PLATFORM="${CMESH_INSTALLER_SMOKE_PLATFORM:-linux/amd64}"
IMAGE="${CMESH_INSTALLER_SMOKE_IMAGE:-ubuntu:24.04}"

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

run_linux_dry_run() {
  local script="$1"
  shift
  docker run --rm --platform "$PLATFORM" -v "$ROOT_DIR":/src -w /src "$IMAGE" \
    sh -lc "$* scripts/$script"
}

main() {
  need docker
  mkdir -p "$WORK_DIR"

  run_linux_dry_run install-manager-linux.sh \
    "CMESH_INSTALL_DRY_RUN=true CMESH_NONINTERACTIVE=true CMESH_DOMAIN=cmesh.example.com CMESH_ADDR=0.0.0.0:19080 CMESH_INSTALL_CADDY=true" \
    >"$WORK_DIR/manager.txt"

  run_linux_dry_run install-manager-linux.sh \
    "CMESH_INSTALL_DRY_RUN=true CMESH_NONINTERACTIVE=true CMESH_BINARY_URL=https://downloads.example.com/cmesh-linux-amd64 CMESH_DOMAIN=cmesh.example.com" \
    >"$WORK_DIR/manager-binary-override.txt"

  run_linux_dry_run install-manager-linux.sh \
    "CMESH_INSTALL_DRY_RUN=true CMESH_NONINTERACTIVE=true CMESH_DOMAIN=cmesh.example.com CMESH_JOIN_TOKEN=test-join-token CMESH_OPERATOR_TOKEN=test-operator-token" \
    >"$WORK_DIR/manager-token-configured.txt"

  run_linux_dry_run install-worker.sh \
    "CMESH_INSTALL_DRY_RUN=true CMESH_NONINTERACTIVE=true CMESH_MANAGER_URL=https://cmesh.example.com CMESH_JOIN_TOKEN=test-token CMESH_CPU=2" \
    >"$WORK_DIR/worker.txt"

  run_linux_dry_run install-worker.sh \
    "CMESH_INSTALL_DRY_RUN=true CMESH_NONINTERACTIVE=true CMESH_BINARY_URL=https://downloads.example.com/cmesh-linux-amd64 CMESH_MANAGER_URL=https://cmesh.example.com CMESH_JOIN_TOKEN=test-token CMESH_CPU=2" \
    >"$WORK_DIR/worker-binary-override.txt"

  run_linux_dry_run install-worker.sh \
    "CMESH_INSTALL_DRY_RUN=true CMESH_NONINTERACTIVE=true CMESH_MANAGER_URL=https://cmesh.example.com CMESH_JOIN_TOKEN=test-token CMESH_CPU=2 CMESH_INSTALL_SERVICE=true CMESH_MODEL_ID=qwen2.5-0.5b-instruct-q4-k-m CMESH_MODEL_URL=https://models.example.com/qwen2.5-0.5b-instruct-q4_k_m.gguf CMESH_MODEL_LAYERS=24" \
    >"$WORK_DIR/worker-stage-model.txt"

  set +e
  docker run --rm --platform "$PLATFORM" -v "$ROOT_DIR":/src -w /src "$IMAGE" \
    sh -lc "scripts/install-manager-linux.sh bad-action" >"$WORK_DIR/manager-bad-action.txt" 2>&1
  bad_action_rc=$?
  set -e
  if [[ "$bad_action_rc" -eq 0 ]]; then
    fail "manager installer bad action unexpectedly succeeded"
  fi
  grep -F "usage: install-manager-linux.sh [install|status|start|stop|restart|uninstall]" "$WORK_DIR/manager-bad-action.txt" >/dev/null ||
    fail "manager installer did not print service action usage"

  set +e
  docker run --rm --platform "$PLATFORM" -v "$ROOT_DIR":/src -w /src "$IMAGE" \
    sh -lc "scripts/install-worker.sh bad-action" >"$WORK_DIR/worker-bad-action.txt" 2>&1
  bad_action_rc=$?
  set -e
  if [[ "$bad_action_rc" -eq 0 ]]; then
    fail "worker installer bad action unexpectedly succeeded"
  fi
  grep -F "usage: install-worker.sh [install|status|start|stop|restart|uninstall]" "$WORK_DIR/worker-bad-action.txt" >/dev/null ||
    fail "worker installer did not print service action usage"

  for marker in NoNewPrivileges=true PrivateTmp=true ProtectSystem=strict ProtectHome=true ReadWritePaths=/var/lib/cmesh; do
    grep -F "$marker" "$ROOT_DIR/scripts/install-manager-linux.sh" >/dev/null ||
      fail "manager systemd unit is missing hardening marker: $marker"
    grep -F "$marker" "$ROOT_DIR/scripts/install-worker.sh" >/dev/null ||
      fail "worker systemd units are missing hardening marker: $marker"
  done
  for marker in User=cmesh Group=cmesh WorkingDirectory=/var/lib/cmesh; do
    grep -F "$marker" "$ROOT_DIR/scripts/install-manager-linux.sh" >/dev/null ||
      fail "manager systemd unit is missing service identity marker: $marker"
    grep -F "$marker" "$ROOT_DIR/scripts/install-worker.sh" >/dev/null ||
      fail "worker systemd units are missing service identity marker: $marker"
  done
  for marker in "systemctl disable --now cmesh.service" "State and tokens were left in /var/lib/cmesh and /etc/cmesh"; do
    grep -F "$marker" "$ROOT_DIR/scripts/install-manager-linux.sh" >/dev/null ||
      fail "manager uninstall path is missing marker: $marker"
  done
  for marker in "systemctl disable --now cmesh-worker.service" "systemctl disable --now cmesh-stage-daemon.service" "rm -f /etc/systemd/system/cmesh-worker.service" "rm -f /etc/systemd/system/cmesh-stage-daemon.service" "After=cmesh-stage-daemon.service"; do
    grep -F "$marker" "$ROOT_DIR/scripts/install-worker.sh" >/dev/null ||
      fail "worker service lifecycle is missing marker: $marker"
  done

  grep -F "binary_url: https://github.com/NythralHome/cmesh/releases/latest/download/cmesh-linux-amd64" "$WORK_DIR/manager.txt" >/dev/null ||
    fail "manager dry-run did not use latest linux/amd64 binary URL"
  grep -F "binary_url: https://downloads.example.com/cmesh-linux-amd64" "$WORK_DIR/manager-binary-override.txt" >/dev/null ||
    fail "manager dry-run did not honor CMESH_BINARY_URL"
  grep -F "public_url: https://cmesh.example.com" "$WORK_DIR/manager.txt" >/dev/null ||
    fail "manager dry-run did not derive public URL from domain"
  grep -F "caddy_upstream: 127.0.0.1:19080" "$WORK_DIR/manager.txt" >/dev/null ||
    fail "manager dry-run did not derive Caddy upstream from CMESH_ADDR"
  grep -F "health_url: http://127.0.0.1:19080/health" "$WORK_DIR/manager.txt" >/dev/null ||
    fail "manager dry-run did not derive health URL from CMESH_ADDR"
  grep -F "join_token: generated_on_install" "$WORK_DIR/manager.txt" >/dev/null ||
    fail "manager dry-run should not require join token"
  grep -F "operator_token: generated_on_install" "$WORK_DIR/manager.txt" >/dev/null ||
    fail "manager dry-run should not require operator token"
  grep -F "join_token: configured" "$WORK_DIR/manager-token-configured.txt" >/dev/null ||
    fail "manager dry-run did not report configured join token"
  grep -F "operator_token: configured" "$WORK_DIR/manager-token-configured.txt" >/dev/null ||
    fail "manager dry-run did not report configured operator token"
  if grep -F "test-join-token" "$WORK_DIR/manager-token-configured.txt" >/dev/null || grep -F "test-operator-token" "$WORK_DIR/manager-token-configured.txt" >/dev/null; then
    fail "manager dry-run printed raw token values"
  fi

  grep -F "binary_url: https://github.com/NythralHome/cmesh/releases/latest/download/cmesh-linux-amd64" "$WORK_DIR/worker.txt" >/dev/null ||
    fail "worker dry-run did not use latest linux/amd64 binary URL"
  grep -F "binary_url: https://downloads.example.com/cmesh-linux-amd64" "$WORK_DIR/worker-binary-override.txt" >/dev/null ||
    fail "worker dry-run did not honor CMESH_BINARY_URL"
  grep -F "runtime_url: https://github.com/NythralHome/cmesh/releases/latest/download/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz" "$WORK_DIR/worker.txt" >/dev/null ||
    fail "worker dry-run did not auto-select linux/amd64 rpc-stage runtime"
  grep -F "runtime_prefer_cache: true" "$WORK_DIR/worker.txt" >/dev/null ||
    fail "worker dry-run did not prefer pinned runtime cache"
  grep -F "service_active_check: 30s" "$WORK_DIR/worker.txt" >/dev/null ||
    fail "worker dry-run did not report service active check"
  grep -F "cache_dir: /var/lib/cmesh/cache" "$WORK_DIR/worker-stage-model.txt" >/dev/null ||
    fail "worker service dry-run did not use service cache dir"
  grep -F "stage_runner_bin: /var/lib/cmesh/cache/runtimes/llama.cpp/llama.cpp-b9704-linux-amd64-rpc-stage/bin/cmesh-stage-runner" "$WORK_DIR/worker-stage-model.txt" >/dev/null ||
    fail "worker dry-run did not derive stage runner path from runtime cache"
  grep -F "stage_daemon: true" "$WORK_DIR/worker-stage-model.txt" >/dev/null ||
    fail "worker service dry-run did not enable stage daemon"
  grep -F "stage_daemon_backend: mock" "$WORK_DIR/worker-stage-model.txt" >/dev/null ||
    fail "worker service dry-run did not report default stage daemon backend"
  grep -F "stage_daemon_url: http://127.0.0.1:19781" "$WORK_DIR/worker-stage-model.txt" >/dev/null ||
    fail "worker service dry-run did not derive local stage daemon URL"
  grep -F "stage_daemon_session_dir: /var/lib/cmesh/stage-sessions" "$WORK_DIR/worker-stage-model.txt" >/dev/null ||
    fail "worker service dry-run did not derive stage daemon session dir"
  grep -F "stage_daemon_session_owner: cmesh:cmesh" "$WORK_DIR/worker-stage-model.txt" >/dev/null ||
    fail "worker service dry-run did not report stage daemon session owner"
  grep -F "stage_daemon_service_command: /usr/local/bin/cmesh stage-runner daemon --addr 127.0.0.1:19781 --session-dir /var/lib/cmesh/stage-sessions --backend mock --runner-bin /var/lib/cmesh/cache/runtimes/llama.cpp/llama.cpp-b9704-linux-amd64-rpc-stage/bin/cmesh-stage-runner" "$WORK_DIR/worker-stage-model.txt" >/dev/null ||
    fail "worker service dry-run did not report stage daemon service command"
  grep -F "stage_daemon_flags: --stage-daemon-url http://127.0.0.1:19781" "$WORK_DIR/worker-stage-model.txt" >/dev/null ||
    fail "worker dry-run did not report stage daemon worker flag"
  grep -F "model_url: https://models.example.com/qwen2.5-0.5b-instruct-q4_k_m.gguf" "$WORK_DIR/worker-stage-model.txt" >/dev/null ||
    fail "worker dry-run did not report model URL"
  grep -F "model_path: /var/lib/cmesh/models/qwen2.5-0.5b-instruct-q4_k_m.gguf" "$WORK_DIR/worker-stage-model.txt" >/dev/null ||
    fail "worker dry-run did not derive service model path"
  grep -F "model_flags: --model-id qwen2.5-0.5b-instruct-q4-k-m --runtime llama.cpp --model-path /var/lib/cmesh/models/qwen2.5-0.5b-instruct-q4_k_m.gguf --model-layers 24" "$WORK_DIR/worker-stage-model.txt" >/dev/null ||
    fail "worker dry-run did not report model override flags"

  cat >"$WORK_DIR/summary.txt" <<EOF
PASS: installer dry-run smoke completed
platform: $PLATFORM
image: $IMAGE
manager: $WORK_DIR/manager.txt
manager_binary_override: $WORK_DIR/manager-binary-override.txt
manager_token_configured: $WORK_DIR/manager-token-configured.txt
worker: $WORK_DIR/worker.txt
worker_binary_override: $WORK_DIR/worker-binary-override.txt
worker_stage_model: $WORK_DIR/worker-stage-model.txt
manager_bad_action: $WORK_DIR/manager-bad-action.txt
worker_bad_action: $WORK_DIR/worker-bad-action.txt
EOF
  cat "$WORK_DIR/summary.txt"
}

main "$@"
