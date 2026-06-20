#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_LLAMA_CPP_RESIDENT_LOOP_SMOKE_DIR:-/tmp/cmesh-llamacpp-resident-loop-smoke-$(date -u +%Y%m%d%H%M%S)}"
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

  python3 - "$runner" "$RUN_DIR/transcript.jsonl" <<'PY'
import json
import subprocess
import sys

runner = sys.argv[1]
transcript = sys.argv[2]

proc = subprocess.Popen(
    [runner, "--command", "resident-loop"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
)

def request(line):
    assert proc.stdin is not None
    assert proc.stdout is not None
    proc.stdin.write(line + "\n")
    proc.stdin.flush()
    raw = proc.stdout.readline()
    if not raw:
        stderr = proc.stderr.read() if proc.stderr else ""
        raise AssertionError(f"resident-loop closed early; stderr={stderr!r}")
    with open(transcript, "a", encoding="utf-8") as f:
        f.write(raw)
    return json.loads(raw)

cap = request("command=capabilities")
assert cap["kind"] == "cmesh.llamacpp_resident_loop_capabilities"
assert cap["protocol"] == "cdip.llamacpp-resident-loop-v1"
assert cap["runner_protocol"] == "cdip.llamacpp-resident-runner-v1"
assert cap["persistent_process"] is True
assert cap["ready"] is True
assert cap["native_kv"] is True
assert cap["persistent_model"] is True
assert cap["persistent_kv_in_memory"] is True
assert cap["prepare_hook"] is True
assert cap["decode_hook"] is True
assert cap["source_decode_hook"] is True
assert cap["relay_decode_hook"] is True
assert cap["terminal_decode_hook"] is True
assert cap["session_count"] == 0

shutdown = request("command=shutdown")
assert shutdown["kind"] == "cmesh.llamacpp_resident_loop_shutdown"
assert shutdown["status"] == "closing"
code = proc.wait(timeout=5)
assert code == 0
PY

  echo "PASS: llama.cpp resident-loop smoke succeeded"
  echo "Evidence: $RUN_DIR"
}

main "$@"
