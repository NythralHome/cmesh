#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PACKAGE_DIR="${CMESH_LINUX_PACKAGE_DIR:-}"
WORK_DIR="${WORK_DIR:-${TMPDIR:-/tmp}/cmesh-linux-production-runtime-smoke-$(date -u +%Y%m%d%H%M%S)}"
PLATFORM="${CMESH_RUNTIME_SMOKE_PLATFORM:-linux/amd64}"
IMAGE="${CMESH_RUNTIME_SMOKE_IMAGE:-ubuntu:24.04}"
RUNTIME_ARCHIVE_NAME="${CMESH_RUNTIME_ARCHIVE_NAME:-llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz}"

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

main() {
  need docker
  mkdir -p "$WORK_DIR"

  if [[ -z "$PACKAGE_DIR" ]]; then
    PACKAGE_DIR="$(latest_package_dir)"
  fi
  [[ -n "$PACKAGE_DIR" ]] || fail "CMESH_LINUX_PACKAGE_DIR is required or dist/linux-production must contain a package"
  PACKAGE_DIR="$(cd "$PACKAGE_DIR" && pwd -P)"

  [[ -f "$PACKAGE_DIR/$RUNTIME_ARCHIVE_NAME" ]] || fail "missing runtime archive: $PACKAGE_DIR/$RUNTIME_ARCHIVE_NAME"
  [[ -f "$PACKAGE_DIR/$RUNTIME_ARCHIVE_NAME.sha256" ]] || fail "missing runtime checksum: $PACKAGE_DIR/$RUNTIME_ARCHIVE_NAME.sha256"
  (
    cd "$PACKAGE_DIR"
    shasum -a 256 -c checksums.txt >/dev/null
  )

  docker run --rm --platform "$PLATFORM" -v "$PACKAGE_DIR":/pkg -v "$WORK_DIR":/work -w /work "$IMAGE" \
    sh -lc "
      set -eu
      apt-get update >/dev/null
      apt-get install -y libgomp1 >/dev/null
      (cd /pkg && sha256sum -c $RUNTIME_ARCHIVE_NAME.sha256) > runtime-sha256-check.txt
      mkdir runtime
      tar -C runtime -xzf /pkg/$RUNTIME_ARCHIVE_NAME
      test -x runtime/bin/llama-cli
      test -x runtime/bin/rpc-server
      test -x runtime/bin/cmesh-stage-runner
      runtime/bin/cmesh-stage-runner --probe > stage-runner-probe.json
      grep -F '\"kind\": \"cmesh.llamacpp_stage_runner_probe\"' stage-runner-probe.json >/dev/null
      runtime/bin/cmesh-stage-runner --command resident-capabilities > resident-capabilities.json
      grep -F '\"protocol\": \"cdip.llamacpp-resident-runner-v1\"' resident-capabilities.json >/dev/null
      printf 'command=capabilities\ncommand=shutdown\n' | runtime/bin/cmesh-stage-runner --command resident-loop > resident-loop.jsonl
      grep -F '\"protocol\":\"cdip.llamacpp-resident-loop-v1\"' resident-loop.jsonl >/dev/null
    "

  cat >"$WORK_DIR/summary.txt" <<EOF
PASS: Linux production runtime smoke completed
package_dir: $PACKAGE_DIR
platform: $PLATFORM
image: $IMAGE
runtime_archive: $RUNTIME_ARCHIVE_NAME
runtime_sha256_check: $WORK_DIR/runtime-sha256-check.txt
stage_runner_probe: $WORK_DIR/stage-runner-probe.json
resident_capabilities: $WORK_DIR/resident-capabilities.json
resident_loop: $WORK_DIR/resident-loop.jsonl
EOF
  cat "$WORK_DIR/summary.txt"
}

main "$@"
