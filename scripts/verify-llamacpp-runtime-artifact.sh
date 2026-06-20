#!/usr/bin/env bash
set -euo pipefail

ARCHIVE="${1:-}"
REQUIRE_STAGE="${CMESH_REQUIRE_STAGE_RUNNER:-false}"
REQUIRE_RESIDENT_PROTOCOL_STATIC="${CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC:-false}"
TARGET_OS=""
TARGET_CPU=""

fail() {
  echo "error: $*" >&2
  exit 1
}

if [[ -z "$ARCHIVE" ]]; then
  fail "usage: $0 path/to/llama.cpp-*.tar.gz"
fi
[[ -f "$ARCHIVE" ]] || fail "archive does not exist: $ARCHIVE"

case "$(basename "$ARCHIVE")" in
  llama.cpp-*-linux-amd64-rpc.tar.gz) TARGET_OS=linux; TARGET_CPU=amd64 ;;
  llama.cpp-*-linux-amd64-rpc-stage.tar.gz) TARGET_OS=linux; TARGET_CPU=amd64; REQUIRE_STAGE=true ;;
  llama.cpp-*-darwin-amd64-rpc.tar.gz) TARGET_OS=darwin; TARGET_CPU=amd64 ;;
  llama.cpp-*-darwin-amd64-rpc-stage.tar.gz) TARGET_OS=darwin; TARGET_CPU=amd64; REQUIRE_STAGE=true ;;
  llama.cpp-*-darwin-arm64-rpc.tar.gz) TARGET_OS=darwin; TARGET_CPU=arm64 ;;
  llama.cpp-*-darwin-arm64-rpc-stage.tar.gz) TARGET_OS=darwin; TARGET_CPU=arm64; REQUIRE_STAGE=true ;;
  *) fail "unexpected runtime archive name: $(basename "$ARCHIVE")" ;;
esac

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/cmesh-runtime-verify-XXXXXX")"
cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

tar -tzf "$ARCHIVE" > "$WORK_DIR/manifest.txt"
grep -Fx './bin/llama-cli' "$WORK_DIR/manifest.txt" >/dev/null || fail "missing bin/llama-cli"
grep -Fx './bin/rpc-server' "$WORK_DIR/manifest.txt" >/dev/null || fail "missing bin/rpc-server"

if [[ "$REQUIRE_STAGE" == "true" ]]; then
  grep -Fx './bin/cmesh-stage-runner' "$WORK_DIR/manifest.txt" >/dev/null || fail "missing bin/cmesh-stage-runner"
fi

tar -C "$WORK_DIR" -xzf "$ARCHIVE"
[[ -x "$WORK_DIR/bin/llama-cli" ]] || fail "bin/llama-cli is not executable"
[[ -x "$WORK_DIR/bin/rpc-server" ]] || fail "bin/rpc-server is not executable"

host_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
host_cpu="$(uname -m)"
case "$host_cpu" in
  x86_64|amd64) host_cpu=amd64 ;;
  arm64|aarch64) host_cpu=arm64 ;;
esac

if [[ "$REQUIRE_STAGE" == "true" ]]; then
	[[ -x "$WORK_DIR/bin/cmesh-stage-runner" ]] || fail "bin/cmesh-stage-runner is not executable"
  if [[ "$host_os" != "$TARGET_OS" || "$host_cpu" != "$TARGET_CPU" ]]; then
    if grep -aF 'cdip.llamacpp-resident-runner-v1' "$WORK_DIR/bin/cmesh-stage-runner" >/dev/null; then
      echo "resident protocol string present in non-native stage runner"
    elif [[ "$REQUIRE_RESIDENT_PROTOCOL_STATIC" == "true" ]]; then
      fail "non-native cmesh-stage-runner is missing resident protocol string cdip.llamacpp-resident-runner-v1"
    else
      echo "warning: non-native cmesh-stage-runner is missing resident protocol string cdip.llamacpp-resident-runner-v1"
      echo "warning: set CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC=true to make this fatal"
    fi
    if grep -aF 'cdip.llamacpp-resident-loop-v1' "$WORK_DIR/bin/cmesh-stage-runner" >/dev/null; then
      echo "resident loop protocol string present in non-native stage runner"
    elif [[ "$REQUIRE_RESIDENT_PROTOCOL_STATIC" == "true" ]]; then
      fail "non-native cmesh-stage-runner is missing resident loop protocol string cdip.llamacpp-resident-loop-v1"
    else
      echo "warning: non-native cmesh-stage-runner is missing resident loop protocol string cdip.llamacpp-resident-loop-v1"
      echo "warning: set CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC=true to make this fatal"
    fi
    if grep -aF 'resident_ready' "$WORK_DIR/bin/cmesh-stage-runner" >/dev/null; then
      echo "resident ready status string present in non-native stage runner"
    elif [[ "$REQUIRE_RESIDENT_PROTOCOL_STATIC" == "true" ]]; then
      fail "non-native cmesh-stage-runner is missing resident ready status string resident_ready"
    else
      echo "warning: non-native cmesh-stage-runner is missing resident ready status string resident_ready"
      echo "warning: set CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC=true to make this fatal"
    fi
    if grep -aF 'payload_bytes' "$WORK_DIR/bin/cmesh-stage-runner" >/dev/null; then
      echo "resident decode payload marker present in non-native stage runner"
    elif [[ "$REQUIRE_RESIDENT_PROTOCOL_STATIC" == "true" ]]; then
      fail "non-native cmesh-stage-runner is missing resident decode payload marker payload_bytes"
    else
      echo "warning: non-native cmesh-stage-runner is missing resident decode payload marker payload_bytes"
      echo "warning: set CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC=true to make this fatal"
    fi
    if grep -aF 'resident_source_decoded' "$WORK_DIR/bin/cmesh-stage-runner" >/dev/null; then
      echo "resident source decode marker present in non-native stage runner"
    elif [[ "$REQUIRE_RESIDENT_PROTOCOL_STATIC" == "true" ]]; then
      fail "non-native cmesh-stage-runner is missing resident source decode marker resident_source_decoded"
    else
      echo "warning: non-native cmesh-stage-runner is missing resident source decode marker resident_source_decoded"
      echo "warning: set CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC=true to make this fatal"
    fi
    if grep -aF 'resident_relay_decoded' "$WORK_DIR/bin/cmesh-stage-runner" >/dev/null; then
      echo "resident relay decode marker present in non-native stage runner"
    elif [[ "$REQUIRE_RESIDENT_PROTOCOL_STATIC" == "true" ]]; then
      fail "non-native cmesh-stage-runner is missing resident relay decode marker resident_relay_decoded"
    else
      echo "warning: non-native cmesh-stage-runner is missing resident relay decode marker resident_relay_decoded"
      echo "warning: set CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC=true to make this fatal"
    fi
    if grep -aF 'resident_terminal_decoded' "$WORK_DIR/bin/cmesh-stage-runner" >/dev/null; then
      echo "resident terminal decode marker present in non-native stage runner"
    elif [[ "$REQUIRE_RESIDENT_PROTOCOL_STATIC" == "true" ]]; then
      fail "non-native cmesh-stage-runner is missing resident terminal decode marker resident_terminal_decoded"
    else
      echo "warning: non-native cmesh-stage-runner is missing resident terminal decode marker resident_terminal_decoded"
      echo "warning: set CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC=true to make this fatal"
    fi
    echo "skipping stage-runner probe for non-native archive $TARGET_OS/$TARGET_CPU on $host_os/$host_cpu"
    echo "PASS: llama.cpp runtime artifact verified"
    echo "archive: $ARCHIVE"
    exit 0
  fi
	"$WORK_DIR/bin/cmesh-stage-runner" --probe > "$WORK_DIR/cmesh-stage-runner-probe.json"
	grep -F '"kind": "cmesh.llamacpp_stage_runner_probe"' "$WORK_DIR/cmesh-stage-runner-probe.json" >/dev/null || fail "stage runner probe did not return expected kind"
	"$WORK_DIR/bin/cmesh-stage-runner" --command resident-capabilities > "$WORK_DIR/cmesh-stage-runner-resident-capabilities.json"
	grep -F '"kind": "cmesh.llamacpp_resident_capabilities"' "$WORK_DIR/cmesh-stage-runner-resident-capabilities.json" >/dev/null || fail "stage runner resident-capabilities did not return expected kind"
	grep -F '"protocol": "cdip.llamacpp-resident-runner-v1"' "$WORK_DIR/cmesh-stage-runner-resident-capabilities.json" >/dev/null || fail "stage runner resident-capabilities did not return expected protocol"
	printf 'command=capabilities\ncommand=shutdown\n' | "$WORK_DIR/bin/cmesh-stage-runner" --command resident-loop > "$WORK_DIR/cmesh-stage-runner-resident-loop.jsonl"
	grep -F '"kind":"cmesh.llamacpp_resident_loop_capabilities"' "$WORK_DIR/cmesh-stage-runner-resident-loop.jsonl" >/dev/null || fail "stage runner resident-loop did not return expected capabilities kind"
	grep -F '"protocol":"cdip.llamacpp-resident-loop-v1"' "$WORK_DIR/cmesh-stage-runner-resident-loop.jsonl" >/dev/null || fail "stage runner resident-loop did not return expected protocol"
	grep -F '"prepare_hook":true' "$WORK_DIR/cmesh-stage-runner-resident-loop.jsonl" >/dev/null || fail "stage runner resident-loop did not report prepare_hook=true"
	grep -aF 'resident_ready' "$WORK_DIR/bin/cmesh-stage-runner" >/dev/null || fail "stage runner is missing resident ready status string"
	grep -aF 'payload_bytes' "$WORK_DIR/bin/cmesh-stage-runner" >/dev/null || fail "stage runner is missing resident decode payload marker"
	grep -aF 'resident_source_decoded' "$WORK_DIR/bin/cmesh-stage-runner" >/dev/null || fail "stage runner is missing resident source decode marker"
	grep -aF 'resident_relay_decoded' "$WORK_DIR/bin/cmesh-stage-runner" >/dev/null || fail "stage runner is missing resident relay decode marker"
	grep -aF 'resident_terminal_decoded' "$WORK_DIR/bin/cmesh-stage-runner" >/dev/null || fail "stage runner is missing resident terminal decode marker"
fi

echo "PASS: llama.cpp runtime artifact verified"
echo "archive: $ARCHIVE"
