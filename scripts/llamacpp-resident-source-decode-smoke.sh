#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${CMESH_LLAMA_CPP_RESIDENT_SOURCE_DECODE_SMOKE_DIR:-/tmp/cmesh-llamacpp-resident-source-decode-smoke-$(date -u +%Y%m%d%H%M%S)}"
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
    echo "SKIP: set CMESH_GGUF_MODEL_PATH or CMESH_DOWNLOAD_GGUF_FIXTURE=1 to run resident source decode smoke" >&2
    exit 0
  fi

  (
    cd "$ROOT_DIR"
    WORK_DIR="$WORK_DIR" JOBS="${JOBS:-2}" scripts/prepare-llamacpp-stage-runner-worktree.sh > "$RUN_DIR/build.log"
  )

  local runner output_file relay_output_file prompt_file prompt_output_file
  runner="$WORK_DIR/src/build-cmesh-stage/bin/cmesh-stage-runner"
  output_file="$RUN_DIR/source-output.bin"
  relay_output_file="$RUN_DIR/relay-output.bin"
  prompt_file="$RUN_DIR/prompt.txt"
  prompt_output_file="$RUN_DIR/source-prompt-output.bin"
  printf 'hello resident source stage' > "$prompt_file"
  [[ -x "$runner" ]] || {
    echo "FAIL: missing stage runner binary: $runner" >&2
    exit 1
  }

  python3 - "$runner" "$model_path" "$output_file" "$relay_output_file" "$prompt_file" "$prompt_output_file" "$RUN_DIR/transcript.jsonl" <<'PY'
import json
import os
import subprocess
import sys

runner = sys.argv[1]
model_path = sys.argv[2]
output_file = sys.argv[3]
relay_output_file = sys.argv[4]
prompt_file = sys.argv[5]
prompt_output_file = sys.argv[6]
transcript = sys.argv[7]

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
    assert cap["prepare_hook"] is True

    prep = request(
        "command=prepare "
        f"session_id=stage-0 model={model_path} "
        "stage_index=0 stage_start=0 stage_end=0 native_prepare=1 ctx=64"
    )
    assert prep["status"] == "resident_ready", prep
    assert prep["persistent_model"] is True
    assert prep["persistent_kv_in_memory"] is True
    n_embd = int(prep["n_embd"])
    n_layer = int(prep["n_layer"])
    assert n_embd > 0
    assert n_layer > 2

    dec1 = request(
        "command=decode session_id=stage-0 stage_command=source_decode "
        f"step=1 token_id=1 output_file={output_file}"
    )
    assert dec1["kind"] == "cmesh.llamacpp_resident_loop_decode"
    assert dec1["status"] == "resident_source_decoded", dec1
    assert dec1["decode_status"] == 0
    assert dec1["position_offset"] == 0
    assert dec1["token_count"] == 1
    assert dec1["persistent_model"] is True
    assert dec1["persistent_kv_in_memory"] is True
    assert dec1["output_file"] == output_file
    assert dec1["output_bytes"] == n_embd * 4
    assert os.path.getsize(output_file) == n_embd * 4

    dec2 = request(
        "command=decode session_id=stage-0 stage_command=source_decode "
        f"step=2 token_id=1 output_file={output_file}"
    )
    assert dec2["status"] == "resident_source_decoded", dec2
    assert dec2["decode_status"] == 0
    assert dec2["position_offset"] == 1
    assert dec2["token_count"] == 2

    prep_relay = request(
        "command=prepare "
        f"session_id=stage-1 model={model_path} "
        "stage_index=1 stage_start=1 stage_end=1 native_prepare=1 ctx=64"
    )
    assert prep_relay["status"] == "resident_ready", prep_relay
    relay = request(
        "command=decode session_id=stage-1 stage_command=relay_decode "
        f"step=1 activation_file={output_file} dtype=f32 shape=1,1,{n_embd} output_file={relay_output_file}"
    )
    assert relay["kind"] == "cmesh.llamacpp_resident_loop_decode"
    assert relay["status"] == "resident_relay_decoded", relay
    assert relay["input_mode"] == "activation_file", relay
    assert relay["decode_status"] == 0
    assert relay["position_offset"] == 0
    assert relay["output_bytes"] == n_embd * 4
    assert os.path.getsize(relay_output_file) == n_embd * 4

    prep_terminal = request(
        "command=prepare "
        f"session_id=stage-terminal model={model_path} "
        f"stage_index=2 stage_start=2 stage_end={n_layer - 1} native_prepare=1 ctx=64"
    )
    assert prep_terminal["status"] == "resident_ready", prep_terminal
    terminal = request(
        "command=decode session_id=stage-terminal stage_command=terminal_decode "
        f"step=1 activation_file={relay_output_file} dtype=f32 shape=1,1,{n_embd}"
    )
    assert terminal["kind"] == "cmesh.llamacpp_resident_loop_decode"
    assert terminal["status"] == "resident_terminal_decoded", terminal
    assert terminal["input_mode"] == "activation_file", terminal
    assert terminal["decode_status"] == 0
    assert terminal["position_offset"] == 0
    assert terminal["token_count"] == 1
    assert terminal["next_token_id"] >= 0
    assert "next_token_text" in terminal

    close_terminal = request("command=complete session_id=stage-terminal")
    assert close_terminal["closed"] is True

    close_relay = request("command=complete session_id=stage-1")
    assert close_relay["closed"] is True

    close = request("command=complete session_id=stage-0")
    assert close["closed"] is True

    prep_prompt = request(
        "command=prepare "
        f"session_id=stage-0-prompt model={model_path} "
        "stage_index=0 stage_start=0 stage_end=0 native_prepare=1 ctx=64"
    )
    assert prep_prompt["status"] == "resident_ready", prep_prompt
    n_embd_prompt = int(prep_prompt["n_embd"])
    prompt_dec = request(
        "command=decode session_id=stage-0-prompt stage_command=source_decode "
        f"step=1 prompt_file={prompt_file} output_file={prompt_output_file}"
    )
    assert prompt_dec["status"] == "resident_source_decoded", prompt_dec
    assert prompt_dec["input_mode"] == "prompt_file", prompt_dec
    assert prompt_dec["decode_status"] == 0
    assert prompt_dec["position_offset"] == 0
    assert prompt_dec["token_count"] > 1
    assert prompt_dec["output_bytes"] == int(prompt_dec["token_count"]) * n_embd_prompt * 4
    assert os.path.getsize(prompt_output_file) == prompt_dec["output_bytes"]

    close_prompt = request("command=complete session_id=stage-0-prompt")
    assert close_prompt["closed"] is True

    shutdown = request("command=shutdown")
    assert shutdown["status"] == "closing"
    code = proc.wait(timeout=5)
    assert code == 0
finally:
    if proc.poll() is None:
        proc.kill()
        proc.wait(timeout=5)
PY

  echo "PASS: llama.cpp resident source-decode smoke succeeded"
  echo "Evidence: $RUN_DIR"
}

main "$@"
