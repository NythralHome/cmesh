#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PACKAGE_DIR="${CMESH_LINUX_PACKAGE_DIR:-}"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-linux-production-manager-install-smoke-$(date -u +%Y%m%d%H%M%S)}"
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
  for asset in cmesh-linux-amd64 cmesh-linux-arm64 install-manager-linux.sh install-worker.sh manifest.json checksums.txt; do
    [[ -f "$PACKAGE_DIR/$asset" ]] || fail "package is missing $asset"
  done
  compgen -G "$PACKAGE_DIR/llama.cpp-*-linux-amd64-rpc-stage.tar.gz" >/dev/null ||
    fail "package is missing linux amd64 stage runtime archive"
  (
    cd "$PACKAGE_DIR"
    shasum -a 256 -c checksums.txt >/dev/null
  )
}

run_manager_dry_run() {
  local env_prefix="$1"
  local output="$2"
  docker run --rm --platform "$PLATFORM" -v "$PACKAGE_DIR":/pkg -w /pkg "$IMAGE" \
    sh -lc "$env_prefix ./install-manager-linux.sh install" >"$output"
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

  run_manager_dry_run \
    "CMESH_INSTALL_DRY_RUN=true CMESH_NONINTERACTIVE=true CMESH_VERSION=v0.1.0-test CMESH_DOMAIN=cmesh.example.com CMESH_ADDR=0.0.0.0:19080 CMESH_INSTALL_CADDY=true" \
    "$WORK_DIR/manager-domain.txt"

  run_manager_dry_run \
    "CMESH_INSTALL_DRY_RUN=true CMESH_NONINTERACTIVE=true CMESH_BINARY_URL=file:///pkg/cmesh-linux-amd64 CMESH_ADDR=0.0.0.0:18080 CMESH_PUBLIC_URL=http://manager.example.test:18080 CMESH_JOIN_TOKEN=test-join-token CMESH_OPERATOR_TOKEN=test-operator-token" \
    "$WORK_DIR/manager-local-package-binary.txt"

  set +e
  docker run --rm --platform "$PLATFORM" -v "$PACKAGE_DIR":/pkg -w /pkg "$IMAGE" \
    sh -lc "./install-manager-linux.sh bad-action" >"$WORK_DIR/manager-bad-action.txt" 2>&1
  bad_action_rc=$?
  set -e
  if [[ "$bad_action_rc" -eq 0 ]]; then
    fail "manager installer bad action unexpectedly succeeded"
  fi

  grep -F "binary_url: https://github.com/NythralHome/cmesh/releases/download/v0.1.0-test/cmesh-linux-amd64" "$WORK_DIR/manager-domain.txt" >/dev/null ||
    fail "manager dry-run did not use pinned release linux/amd64 binary URL"
  grep -F "public_url: https://cmesh.example.com" "$WORK_DIR/manager-domain.txt" >/dev/null ||
    fail "manager dry-run did not derive public URL from domain"
  grep -F "caddy_upstream: 127.0.0.1:19080" "$WORK_DIR/manager-domain.txt" >/dev/null ||
    fail "manager dry-run did not derive Caddy upstream from CMESH_ADDR"
  grep -F "health_url: http://127.0.0.1:19080/health" "$WORK_DIR/manager-domain.txt" >/dev/null ||
    fail "manager dry-run did not derive health URL from CMESH_ADDR"
  grep -F "join_token: generated_on_install" "$WORK_DIR/manager-domain.txt" >/dev/null ||
    fail "manager dry-run should generate join token on install"
  grep -F "operator_token: generated_on_install" "$WORK_DIR/manager-domain.txt" >/dev/null ||
    fail "manager dry-run should generate operator token on install"

  grep -F "binary_url: file:///pkg/cmesh-linux-amd64" "$WORK_DIR/manager-local-package-binary.txt" >/dev/null ||
    fail "manager dry-run did not honor package-local CMESH_BINARY_URL"
  grep -F "join_token: configured" "$WORK_DIR/manager-local-package-binary.txt" >/dev/null ||
    fail "manager dry-run did not report configured join token"
  grep -F "operator_token: configured" "$WORK_DIR/manager-local-package-binary.txt" >/dev/null ||
    fail "manager dry-run did not report configured operator token"
  if grep -F "test-join-token" "$WORK_DIR/manager-local-package-binary.txt" >/dev/null ||
    grep -F "test-operator-token" "$WORK_DIR/manager-local-package-binary.txt" >/dev/null; then
    fail "manager dry-run printed raw token values"
  fi

  grep -F "usage: install-manager-linux.sh [install|status|start|stop|restart|uninstall]" "$WORK_DIR/manager-bad-action.txt" >/dev/null ||
    fail "manager installer did not print bad-action usage"

  for marker in \
    "User=cmesh" \
    "Group=cmesh" \
    "WorkingDirectory=/var/lib/cmesh" \
    "EnvironmentFile=/etc/cmesh/manager.env" \
    "NoNewPrivileges=true" \
    "PrivateTmp=true" \
    "ProtectSystem=strict" \
    "ProtectHome=true" \
    "ReadWritePaths=/var/lib/cmesh" \
    "chmod 600 /etc/cmesh/manager.env" \
    "wait_for_manager_health"; do
    grep -F "$marker" "$PACKAGE_DIR/install-manager-linux.sh" >/dev/null ||
      fail "packaged manager installer is missing marker: $marker"
  done

  cat >"$WORK_DIR/summary.txt" <<EOF
PASS: Linux production manager install smoke completed
package_dir: $PACKAGE_DIR
platform: $PLATFORM
image: $IMAGE
manager_domain: $WORK_DIR/manager-domain.txt
manager_local_package_binary: $WORK_DIR/manager-local-package-binary.txt
manager_bad_action: $WORK_DIR/manager-bad-action.txt
EOF
  cat "$WORK_DIR/summary.txt"
}

main "$@"
