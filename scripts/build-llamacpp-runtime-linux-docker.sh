#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LLAMA_CPP_REF="${LLAMA_CPP_REF:-b9704}"
TARGET_CPU="${CMESH_STAGE_RUNTIME_TARGET_CPU:-amd64}"
OUT_DIR="${OUT_DIR:-$ROOT_DIR/dist/runtimes-linux-$TARGET_CPU-current}"
WORK_DIR="${WORK_DIR:-/tmp/cmesh-llama.cpp-linux-$TARGET_CPU-build}"
JOBS="${JOBS:-2}"
IMAGE="${CMESH_LLAMA_CPP_DOCKER_IMAGE:-ubuntu:24.04}"

fail() {
  echo "error: $*" >&2
  exit 1
}

[[ "$TARGET_CPU" == "amd64" ]] || fail "only linux/amd64 docker builds are supported for now"
command -v docker >/dev/null 2>&1 || fail "docker is required"

mkdir -p "$OUT_DIR" "$WORK_DIR"

docker run --rm \
  --platform linux/amd64 \
  -e DEBIAN_FRONTEND=noninteractive \
  -e LLAMA_CPP_REF="$LLAMA_CPP_REF" \
  -e CMESH_LLAMA_CPP_STAGE_RUNNER=true \
  -e JOBS="$JOBS" \
  -e WORK_DIR=/work/build \
  -e OUT_DIR=/repo/dist/runtimes-linux-amd64-current \
  -v "$ROOT_DIR:/repo" \
  -v "$WORK_DIR:/work/build" \
  -w /repo \
  "$IMAGE" \
  bash -lc '
    set -euo pipefail
    apt-get update
    apt-get install -y --no-install-recommends ca-certificates git cmake build-essential python3
    scripts/build-llamacpp-runtime.sh
  '

ARCHIVE="$OUT_DIR/llama.cpp-$LLAMA_CPP_REF-linux-amd64-rpc-stage.tar.gz"
"$ROOT_DIR/scripts/verify-llamacpp-runtime-artifact.sh" "$ARCHIVE"
CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC=true "$ROOT_DIR/scripts/verify-llamacpp-runtime-artifact.sh" "$ARCHIVE"

echo "$ARCHIVE"
