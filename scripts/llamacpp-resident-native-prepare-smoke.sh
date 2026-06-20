#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_LLAMA_CPP_RESIDENT_NATIVE_PREPARE_SMOKE_DIR:-/tmp/cmesh-llamacpp-resident-native-prepare-smoke-$(date -u +%Y%m%d%H%M%S)}"
WORK_DIR="$RUN_DIR/llama-worktree"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "FAIL: $1 is required" >&2
    exit 1
  }
}

main() {
  need python3
  need cmake
  need git

  mkdir -p "$RUN_DIR"

  local model_path
  if ! model_path="$(cd "$ROOT_DIR" && scripts/ensure-gguf-fixture.sh)"; then
    echo "SKIP: set CMESH_GGUF_MODEL_PATH or CMESH_DOWNLOAD_GGUF_FIXTURE=1 to run native resident prepare smoke" >&2
    exit 0
  fi

  (
    cd "$ROOT_DIR"
    WORK_DIR="$WORK_DIR" JOBS="${JOBS:-2}" scripts/prepare-llamacpp-stage-runner-worktree.sh > "$RUN_DIR/build.log"
  )

  local runner
  runner="$WORK_DIR/src/build-cmesh-stage/bin/cmesh-stage-runner"
  [[ -x "$runner" ]] || {
    echo "FAIL: missing stage runner binary: $runner" >&2
    exit 1
  }

  python3 - "$runner" "$model_path" "$RUN_DIR/transcript.jsonl" <<'PY'
import json
import subprocess
import sys

runner = sys.argv[1]
model_path = sys.argv[2]
transcript = sys.argv[3]

proc = subprocess.Popen(
    [runner, "--command", "resident-loop"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.DEVNULL,
    text=True,
)

def request(line):
    assert proc.stdin is not None
    assert proc.stdout is not None
    proc.stdin.write(line + "\n")
    proc.stdin.flush()
    raw = proc.stdout.readline()
    if not raw:
        raise AssertionError("resident-loop closed early")
    with open(transcript, "a", encoding="utf-8") as f:
        f.write(raw)
    return json.loads(raw)

try:
    cap = request("command=capabilities")
    assert cap["kind"] == "cmesh.llamacpp_resident_loop_capabilities"
    assert cap["protocol"] == "cdip.llamacpp-resident-loop-v1"
    assert cap["runner_protocol"] == "cdip.llamacpp-resident-runner-v1"
    assert cap["persistent_process"] is True
    assert cap["prepare_hook"] is True
    assert cap["decode_hook"] is True
    assert cap["source_decode_hook"] is True
    assert cap["relay_decode_hook"] is True
    assert cap["terminal_decode_hook"] is True
    assert cap["ready"] is True
    assert cap["session_count"] == 0

    prep = request(
        "command=prepare "
        f"session_id=stage-0 model={model_path} "
        "stage_index=0 stage_start=0 stage_end=0 native_prepare=1 ctx=64"
    )
    assert prep["kind"] == "cmesh.llamacpp_resident_loop_prepare"
    assert prep["protocol"] == "cdip.llamacpp-resident-loop-v1"
    assert prep["status"] == "resident_ready", prep
    assert prep["native_prepare_requested"] is True
    assert prep["session_registered"] is True
    assert prep["persistent_model"] is True
    assert prep["persistent_kv_in_memory"] is True
    assert prep["n_layer"] > 0
    assert prep["n_embd"] > 0
    assert prep["selected_tensor_count"] > 0
    assert prep["selected_bytes"] > 0
    assert prep["session_count"] == 1

    dec = request("command=decode session_id=stage-0 stage_command=source_decode step=1")
    assert dec["kind"] == "cmesh.llamacpp_resident_loop_decode"
    assert dec["status"] == "invalid_token_id"
    assert dec["session_found"] is True
    assert dec["decode_steps"] == 1
    assert dec["persistent_model"] is True
    assert dec["persistent_kv_in_memory"] is True
    assert "token_id or prompt_file" in dec["error"]

    close = request("command=complete session_id=stage-0")
    assert close["kind"] == "cmesh.llamacpp_resident_loop_session_close"
    assert close["closed"] is True
    assert close["session_count"] == 0

    shutdown = request("command=shutdown")
    assert shutdown["kind"] == "cmesh.llamacpp_resident_loop_shutdown"
    assert shutdown["status"] == "closing"
    code = proc.wait(timeout=5)
    assert code == 0
finally:
    if proc.poll() is None:
        proc.kill()
        proc.wait(timeout=5)
PY

  echo "PASS: llama.cpp resident native-prepare smoke succeeded"
  echo "Evidence: $RUN_DIR"
}

main "$@"
