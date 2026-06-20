#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PACKAGE_DIR="${CMESH_LINUX_PACKAGE_DIR:-}"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-linux-production-worker-install-smoke-$(date -u +%Y%m%d%H%M%S)}"
PLATFORM="${CMESH_INSTALLER_SMOKE_PLATFORM:-linux/amd64}"
IMAGE="${CMESH_INSTALLER_SMOKE_IMAGE:-ubuntu:24.04}"

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

latest_package_dir() {
  find "$ROOT_DIR/dist/linux-production" -mindepth 1 -maxdepth 1 -type d 2>/dev/null |
    sort |
    tail -n 1
}

verify_package() {
  [[ -d "$PACKAGE_DIR" ]] || fail "package directory does not exist: $PACKAGE_DIR"
  for asset in cmesh-linux-amd64 install-worker.sh manifest.json checksums.txt; do
    [[ -f "$PACKAGE_DIR/$asset" ]] || fail "package is missing $asset"
  done
  [[ -f "$PACKAGE_DIR/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz" ]] ||
    fail "package is missing linux amd64 stage runtime archive"
  [[ -f "$PACKAGE_DIR/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz.sha256" ]] ||
    fail "package is missing linux amd64 stage runtime checksum"
  (
    cd "$PACKAGE_DIR"
    shasum -a 256 -c checksums.txt >/dev/null
  )
}

run_worker_dry_run() {
  local env_prefix="$1"
  local output="$2"
  docker run --rm --platform "$PLATFORM" -v "$PACKAGE_DIR":/pkg -w /pkg "$IMAGE" \
    sh -lc "$env_prefix ./install-worker.sh install" >"$output"
}

main() {
  need docker
  mkdir -p "$WORK_DIR"

  if [[ -z "$PACKAGE_DIR" ]]; then
    PACKAGE_DIR="$(latest_package_dir)"
  fi
  [[ -n "$PACKAGE_DIR" ]] || fail "CMESH_LINUX_PACKAGE_DIR is required or dist/linux-production must contain a package"
  PACKAGE_DIR="$(cd "$PACKAGE_DIR" && pwd -P)"
  verify_package

  run_worker_dry_run \
    "CMESH_INSTALL_DRY_RUN=true CMESH_NONINTERACTIVE=true CMESH_BINARY_URL=file:///pkg/cmesh-linux-amd64 CMESH_MANAGER_URL=https://cmesh.example.com CMESH_JOIN_TOKEN=test-token CMESH_INSTALL_SERVICE=true CMESH_STAGE_DAEMON=true CMESH_STAGE_DAEMON_BACKEND=llama.cpp-resident CMESH_LLAMA_CPP_RUNTIME_AUTO=true CMESH_LLAMA_CPP_RUNTIME_URL=file:///pkg/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz CMESH_CPU=2 CMESH_MEMORY_GB=6 CMESH_DISK_GB=40 CMESH_MODEL_ID=qwen2.5-14b-instruct-q4-k-m CMESH_MODEL_URL=https://models.example.com/Qwen2.5-14B-Instruct-Q4_K_M.gguf CMESH_MODEL_LAYERS=48" \
    "$WORK_DIR/worker-service-stage-daemon.txt"

  run_worker_dry_run \
    "CMESH_INSTALL_DRY_RUN=true CMESH_NONINTERACTIVE=true CMESH_MANAGER_URL=https://cmesh.example.com CMESH_JOIN_TOKEN=test-token CMESH_CPU=2" \
    "$WORK_DIR/worker-latest-defaults.txt"

  set +e
  docker run --rm --platform "$PLATFORM" -v "$PACKAGE_DIR":/pkg -w /pkg "$IMAGE" \
    sh -lc "./install-worker.sh bad-action" >"$WORK_DIR/worker-bad-action.txt" 2>&1
  bad_action_rc=$?
  set -e
  if [[ "$bad_action_rc" -eq 0 ]]; then
    fail "worker installer bad action unexpectedly succeeded"
  fi

  grep -F "binary_url: file:///pkg/cmesh-linux-amd64" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not honor package-local CMESH_BINARY_URL"
  grep -F "manager_url: https://cmesh.example.com" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not report manager URL"
  grep -F "install_service: true" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not report service install"
  grep -F "cache_dir: /var/lib/cmesh/cache" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker service dry-run did not use service cache dir"
  grep -F "runtime_url: file:///pkg/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not report package-local runtime URL"
  grep -F "runtime_name: llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not infer runtime name from URL"
  grep -F "runtime_version: llama.cpp-b9704-linux-amd64-rpc-stage" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not infer runtime version from URL"
  grep -F "runtime_sha256: auto_required" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not require runtime checksum"
  grep -F "runtime_require_checksum: true" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not report runtime checksum requirement"
  grep -F "runtime_system_dependencies: libgomp1" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not report llama.cpp runtime system dependencies"
  grep -F "stage_runner_bin: /var/lib/cmesh/cache/runtimes/llama.cpp/llama.cpp-b9704-linux-amd64-rpc-stage/bin/cmesh-stage-runner" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not derive stage runner path from explicit runtime URL"
  grep -F "stage_daemon_backend: llama.cpp-resident" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not report llama.cpp resident stage daemon backend"
  grep -F "stage_daemon_session_dir: /var/lib/cmesh/stage-sessions" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not report service stage session dir"
  grep -F "stage_daemon_service_command: /usr/local/bin/cmesh stage-runner daemon --addr 127.0.0.1:19781 --session-dir /var/lib/cmesh/stage-sessions --backend llama.cpp-resident --runner-bin /var/lib/cmesh/cache/runtimes/llama.cpp/llama.cpp-b9704-linux-amd64-rpc-stage/bin/cmesh-stage-runner" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not report stage daemon service command"
  grep -F "stage_daemon_flags: --stage-daemon-url http://127.0.0.1:19781" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not report worker stage-daemon flag"
  grep -F "model_flags: --model-id qwen2.5-14b-instruct-q4-k-m --runtime llama.cpp --model-path /var/lib/cmesh/models/Qwen2.5-14B-Instruct-Q4_K_M.gguf --model-layers 48" "$WORK_DIR/worker-service-stage-daemon.txt" >/dev/null ||
    fail "worker dry-run did not report model flags"

  grep -F "binary_url: https://github.com/NythralHome/cmesh/releases/latest/download/cmesh-linux-amd64" "$WORK_DIR/worker-latest-defaults.txt" >/dev/null ||
    fail "worker latest dry-run did not use latest linux/amd64 binary URL"
  grep -F "runtime_url: https://github.com/NythralHome/cmesh/releases/latest/download/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz" "$WORK_DIR/worker-latest-defaults.txt" >/dev/null ||
    fail "worker latest dry-run did not select pinned linux runtime"
  grep -F "runtime_prefer_cache: true" "$WORK_DIR/worker-latest-defaults.txt" >/dev/null ||
    fail "worker latest dry-run did not prefer runtime cache"

  grep -F "usage: install-worker.sh [install|status|start|stop|restart|uninstall]" "$WORK_DIR/worker-bad-action.txt" >/dev/null ||
    fail "worker installer did not print bad-action usage"

  for marker in \
    "User=cmesh" \
    "Group=cmesh" \
    "WorkingDirectory=/var/lib/cmesh" \
    "EnvironmentFile=/etc/cmesh/worker.env" \
    "NoNewPrivileges=true" \
    "PrivateTmp=true" \
    "ProtectSystem=strict" \
    "ProtectHome=true" \
    "ReadWritePaths=/var/lib/cmesh" \
    "chmod 600 /etc/cmesh/worker.env" \
    "wait_for_worker_service" \
    "wait_for_stage_daemon_service" \
    "After=cmesh-stage-daemon.service"; do
    grep -F "$marker" "$PACKAGE_DIR/install-worker.sh" >/dev/null ||
      fail "packaged worker installer is missing marker: $marker"
  done

  cat >"$WORK_DIR/summary.txt" <<EOF
PASS: Linux production worker install smoke completed
package_dir: $PACKAGE_DIR
platform: $PLATFORM
image: $IMAGE
worker_service_stage_daemon: $WORK_DIR/worker-service-stage-daemon.txt
worker_latest_defaults: $WORK_DIR/worker-latest-defaults.txt
worker_bad_action: $WORK_DIR/worker-bad-action.txt
EOF
  cat "$WORK_DIR/summary.txt"
}

main "$@"
