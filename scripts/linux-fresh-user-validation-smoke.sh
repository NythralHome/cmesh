#!/usr/bin/env bash
set -euo pipefail

CMESH_LINUX_PACKAGE_DIR="${CMESH_LINUX_PACKAGE_DIR:-${1:-}}"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-linux-fresh-user-validation-$(date -u +%Y%m%d%H%M%S)}"
PLATFORM="${CMESH_FRESH_USER_PLATFORM:-linux/amd64}"
IMAGE="${CMESH_FRESH_USER_IMAGE:-ubuntu:24.04}"

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

main() {
  need docker

  [[ -n "$CMESH_LINUX_PACKAGE_DIR" ]] || fail "CMESH_LINUX_PACKAGE_DIR is required"
  CMESH_LINUX_PACKAGE_DIR="$(cd "$CMESH_LINUX_PACKAGE_DIR" && pwd -P)"
  local release_dir="$(dirname "$CMESH_LINUX_PACKAGE_DIR")"
  local package_name="$(basename "$CMESH_LINUX_PACKAGE_DIR")"
  local tarball="$release_dir/$package_name.tar.gz"
  [[ -f "$tarball" ]] || fail "missing signed tarball: $tarball"
  [[ -f "$tarball.sha256" ]] || fail "missing signed tarball checksum: $tarball.sha256"
  [[ -f "$tarball.sig" ]] || fail "missing signed tarball signature: $tarball.sig"
  [[ -f "$tarball.public-key.pem" ]] || fail "missing signed tarball public key: $tarball.public-key.pem"

  mkdir -p "$WORK_DIR"
  docker run --rm --platform "$PLATFORM" \
    -v "$release_dir":/artifacts:ro \
    -v "$WORK_DIR":/evidence \
    "$IMAGE" \
    sh -lc "
      set -eu
      export DEBIAN_FRONTEND=noninteractive
      apt-get update >/dev/null
      apt-get install -y ca-certificates curl jq openssl perl >/dev/null
      cd /artifacts
      openssl dgst -sha256 -verify '$package_name.tar.gz.public-key.pem' -signature '$package_name.tar.gz.sig' '$package_name.tar.gz'
      shasum -a 256 -c '$package_name.tar.gz.sha256'
      mkdir -p /tmp/cmesh-fresh-user
      tar -C /tmp/cmesh-fresh-user -xzf '$package_name.tar.gz'
      cd '/tmp/cmesh-fresh-user/$package_name'
      openssl dgst -sha256 -verify release-signing-public-key.pem -signature manifest.json.sig manifest.json
      openssl dgst -sha256 -verify release-signing-public-key.pem -signature checksums.txt.sig checksums.txt
      shasum -a 256 -c checksums.txt
      jq -e '.kind == \"cmesh.linux.production.release.v1\"' manifest.json >/dev/null
      grep -F 'docs/LINUX_PRODUCTION.md' README.md >/dev/null
      CMESH_INSTALL_DRY_RUN=true CMESH_NONINTERACTIVE=true CMESH_BINARY_URL=file:///tmp/cmesh-fresh-user/$package_name/cmesh-linux-amd64 CMESH_ADDR=0.0.0.0:18080 CMESH_PUBLIC_URL=http://fresh-user-manager:18080 CMESH_JOIN_TOKEN=fresh-user-join CMESH_OPERATOR_TOKEN=fresh-user-operator ./install-manager-linux.sh install > /evidence/manager-install.txt
      CMESH_INSTALL_DRY_RUN=true CMESH_NONINTERACTIVE=true CMESH_BINARY_URL=file:///tmp/cmesh-fresh-user/$package_name/cmesh-linux-amd64 CMESH_MANAGER_URL=http://fresh-user-manager:18080 CMESH_JOIN_TOKEN=fresh-user-join CMESH_INSTALL_SERVICE=true CMESH_STAGE_DAEMON=true CMESH_STAGE_DAEMON_BACKEND=llama.cpp-resident CMESH_LLAMA_CPP_RUNTIME_AUTO=true CMESH_LLAMA_CPP_RUNTIME_URL=file:///tmp/cmesh-fresh-user/$package_name/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM=true CMESH_CPU=2 CMESH_MEMORY_GB=6 CMESH_DISK_GB=40 ./install-worker.sh install > /evidence/worker-install.txt
      grep -F 'operator_token: configured' /evidence/manager-install.txt >/dev/null
      grep -F 'join_token: configured' /evidence/manager-install.txt >/dev/null
      grep -F 'runtime_require_checksum: true' /evidence/worker-install.txt >/dev/null
      grep -F 'stage_daemon_backend: llama.cpp-resident' /evidence/worker-install.txt >/dev/null
      grep -F 'stage_runner_bin: /var/lib/cmesh/cache/runtimes/llama.cpp/llama.cpp-b9704-linux-amd64-rpc-stage/bin/cmesh-stage-runner' /evidence/worker-install.txt >/dev/null
    "

  cat >"$WORK_DIR/summary.txt" <<EOF
PASS: Linux fresh-user validation smoke completed
package_dir: $CMESH_LINUX_PACKAGE_DIR
tarball: $tarball
platform: $PLATFORM
image: $IMAGE
manager_install: $WORK_DIR/manager-install.txt
worker_install: $WORK_DIR/worker-install.txt
EOF
  cat "$WORK_DIR/summary.txt"
}

main "$@"
