# CMesh llama.cpp Stage Runner

This directory contains the first CMesh-owned scaffold for a llama.cpp stage runner.

It is intentionally not a complete layer-sharding implementation yet. The binary builds inside a pinned llama.cpp checkout, can prepare selected stage tensors, run a first-stage prompt-to-hidden bridge, run a local file-based relay decode bridge, export a greedy token from terminal-stage logits, and persist native llama.cpp sequence state to a per-stage file between decode-loop invocations. It remains blocked for complete production distributed inference until automatic runner/model discovery, long-lived stage sessions, and remote multi-machine validation are implemented.

Current purpose:

- create a concrete patch surface in llama.cpp: `tools/cmesh-stage-runner`;
- verify CMake integration against the pinned llama.cpp ref;
- keep the CMesh CDIP command/result vocabulary aligned with the future native stage runner;
- preserve the guardrail that current CDIP tensor envelopes are protocol scaffolding, not real model layer outputs.

Implemented patch hooks:

- selected tensor materialization plan;
- CMesh shard bundle writer for selected tensor payload extraction;
- CMesh shard bundle reader and payload boundary inspector;
- CMesh shard tensor lookup and payload extractor;
- CMesh shard tensor source byte verifier;
- CMesh shard bundle source byte verifier;
- CMesh selected tensor GGUF shard writer;
- Qwen2 first-stage hidden-state output tensor marker;
- Qwen2 middle-stage hidden-state graph input via `ubatch.embd`;
- file-based prompt tokens to first-stage hidden-state output bridge;
- file-based activation payload to `llama_batch.embd` decode bridge;
- local hidden-state output extraction to F32 activation file.
- terminal-stage greedy token export from hidden-state input.
- file-backed native sequence state load/save through `llama_state_seq_load_file`
  and `llama_state_seq_save_file` when `CMESH_STAGE_SESSION_FILE` is set.
- resident-loop long-lived process transport scaffold.

Required future hooks:

- automatic manager/worker discovery for stage runner and model paths;
- native model/context construction inside `resident-loop`;
- resident-loop `llama_decode` dispatch with per-stage KV ownership;
- remote multi-machine stage decode loop validation.

Expected lifecycle:

```text
cmesh-stage-runner --command prepare --model model.gguf --stage-start 0 --stage-end 15 --stage-index 0
cmesh-stage-runner --command prepare --model model.gguf --stage-start 0 --stage-end 15 --stage-index 0 --emit-tensor-list
cmesh-stage-runner --command prepare --model model.gguf --stage-start 0 --stage-end 15 --stage-index 0 --emit-tensor-list --materialize-selected-tensors
cmesh-stage-runner --command write-shard-bundle --model model.gguf --stage-start 0 --stage-end 15 --stage-index 0 --first-stage --output-file stage-0.cmesh-shard
cmesh-stage-runner --command inspect-shard-bundle --bundle-file stage-0.cmesh-shard
cmesh-stage-runner --command extract-shard-tensor --bundle-file stage-0.cmesh-shard --tensor-name token_embd.weight --output-file token_embd.weight.bin
cmesh-stage-runner --command verify-shard-tensor-source --bundle-file stage-0.cmesh-shard --model model.gguf --tensor-name token_embd.weight
cmesh-stage-runner --command verify-shard-bundle-source --bundle-file stage-0.cmesh-shard --model model.gguf
cmesh-stage-runner --command write-stage-gguf-shard --bundle-file stage-0.cmesh-shard --model model.gguf --output-file stage-0.gguf
cmesh-stage-runner --command probe-stage-gguf-load --model stage-0.gguf
cmesh-stage-runner --command source-decode --model stage-0.gguf --stage-start 0 --stage-end 15 --prompt "hello" --output-file source.bin
cmesh-stage-runner --command source-decode --model model.gguf --stage-start 0 --stage-end 15 --prompt "hello" --output-file source.bin
cmesh-stage-runner --command decode --model model.gguf --stage-start 1 --stage-end 15 --activation-file in.bin --dtype f16 --shape 1,1,896 --output-file out.bin
cmesh-stage-runner --command terminal-decode --model model.gguf --stage-start 16 --stage-end 31 --activation-file in.bin --dtype f32 --shape 1,1,896
cmesh-stage-runner --command resident-capabilities
cmesh-stage-runner --command resident-decode --session-id stage-0 --model model.gguf --stage-command relay_decode --activation-file in.bin --dtype f32 --shape 1,1,896 --stage-start 0 --stage-end 15 --stage-index 0 --step 1
cmesh-stage-runner --command resident-loop
cmesh-stage-runner --command prefill --input stage.json
cmesh-stage-runner --command complete --input stage.json
cmesh-stage-runner --command abort --input stage.json
```

`prepare` can already load a GGUF model through llama.cpp public API and emit metadata:

- model name / architecture / description;
- layer count;
- embedding dimensions;
- model size and parameter count;
- validated stage layer range;
- expected hidden-state shape template;
- GGUF tensor manifest for the selected layer range, including first-stage input
  boundary tensors and terminal-stage output boundary tensors when applicable;
- optional full selected tensor allowlist with `--emit-tensor-list`.
- optional selected tensor materialization probe with `--materialize-selected-tensors`.

The tensor manifest is metadata-only. With the CMesh llama.cpp patch applied,
`--materialize-selected-tensors` also performs a selected tensor load probe by
passing the full allowlist through `llama_model_params`.

`write-shard-bundle` is the first physical tensor extraction proof. It writes a
CMesh-owned binary bundle containing a JSON header and the raw selected tensor
payload bytes copied from the source GGUF. This is not a standalone/loadable
GGUF shard yet; it deliberately reports `loadable_gguf=false` so the next step
can focus on converting the bundle layout into a proper GGUF shard writer.

`inspect-shard-bundle` is the matching reader proof. It opens a `.cmesh-shard`,
validates the magic header, reads the JSON header, computes the payload offset,
and verifies that payload bytes match the selected tensor byte count. This is
the adapter boundary needed before teaching llama.cpp to load the shard without
the original full GGUF.

`extract-shard-tensor` proves tensor lookup against the CMesh bundle header. It
finds a tensor by name, seeks to its payload range inside the bundle, and writes
the raw tensor payload to a separate file. This still does not make the bundle a
standalone GGUF, but it proves the next loader primitive: CMesh can serve tensor
bytes from a physical shard artifact without reading those bytes from the
original full model file.

`verify-shard-tensor-source` compares one tensor payload in the CMesh bundle
against the same tensor range in the source GGUF, streaming the comparison in
chunks. This proves that the physical shard artifact carries byte-identical
weights for selected tensors. The source GGUF is only used as a reference in
this verifier; the next loader step is to remove that dependency from execution.

`verify-shard-bundle-source` extends that proof to every tensor recorded in the
bundle header. It streams through all selected tensor ranges and verifies that
the aggregate tensor count and bytes match the CMesh bundle manifest. Passing
this verifier is the current strongest evidence that the physical shard artifact
is byte-correct, even though it is still not a standalone GGUF.

`write-stage-gguf-shard` converts a verified CMesh bundle into a standalone GGUF
container that carries the source metadata plus only the selected tensor payloads
for that stage. The output can be reopened by the GGUF parser, but it is not a
full llama.cpp model load because upstream model loaders expect all required
tensors unless the partial loader hook is active for the selected stage.

`probe-stage-gguf-load` is the guardrail for that boundary. It attempts
`llama_model_load_from_file` against the selected-tensor GGUF using the CMesh
stage metadata and embedded tensor-name allowlist. Passing this probe proves
the artifact can become a resident stage model. It still reports
`loadable_full_model=false` because the file is valid through the CMesh
partial-loader path, not as a normal full model.

`source-decode` can now execute a first-stage prompt-to-hidden bridge: it
tokenizes a prompt or accepts `--token-id` for a follow-up autoregressive
decode step, executes the selected first-stage range, and writes an F32
hidden-state activation file. When the model path is a CMesh stage GGUF, the
runner reads `cmesh.shard.layer_start`, `cmesh.shard.layer_end`, and the
embedded tensor names to load only that selected stage artifact instead of the
original full GGUF.

`decode` can now execute a local file-based activation bridge: it reads an F16
or F32 activation payload, expands it into `llama_batch.embd`, calls
`llama_decode`, and writes F32 hidden output bytes.

`terminal-decode` reads the final hidden activation, executes the terminal stage
range, reads logits from llama.cpp, and returns the greedy next token candidate.
When `CMESH_STAGE_SESSION_FILE` points at a stage-local `.seq` path, source,
relay, and terminal decode commands load the previous native llama.cpp sequence
state before decode, continue from the max stored sequence position, and save
the updated sequence state after decode. This gives CMesh a real file-backed KV
continuity proof across runner process invocations. It is still not complete
production distributed inference because the stage runtime is not yet a
long-lived daemon that owns KV in memory.

For orchestration smoke tests only, `CMESH_TERMINAL_FORCE_FINAL=false` can force
`terminal-decode` to emit `final:false`. CMesh uses this to prove that a worker
reported partial terminal chunk keeps the parent job open, schedules the next
decode-loop wave with the same `kv_cache_key`, carries `next_token_id` back to
the next source stage as `previous_token_id`, and later completes from a
follow-up terminal job. This flag must not be treated as production token-loop
semantics by itself; production token loops should use the file-backed sequence
state path now and eventually a long-lived stage daemon.

`resident-capabilities` declares the production daemon boundary consumed by
`cmesh stage-runner daemon --backend llama.cpp-resident`. The current runner
returns `protocol=cdip.llamacpp-resident-runner-v1` with `ready=false` because
the command surface exists but the native in-process llama.cpp context and
per-stage KV ownership hooks are not implemented yet.

`resident-decode` is present as the future decode entrypoint for that native
runner. The CMesh daemon passes `stage_command`, tensor dtype/shape, and an
activation payload file for relay/terminal stages. Today the runner returns
`blocked_missing_native_hooks` and a non-zero exit code, so CMesh cannot
accidentally claim resident sliced execution from this scaffold.

`resident-loop` is the next production-shaped runner boundary. It keeps one
runner process alive, accepts simple line-based requests on stdin, and returns
JSON lines on stdout. Supported requests are:

```text
command=capabilities
command=prepare session_id=stage-0 model=/path/stage-0.gguf stage_index=0 stage_start=0 stage_end=7
command=prepare session_id=stage-0 model=/path/stage-0.gguf stage_index=0 stage_start=0 stage_end=7 native_prepare=1 ctx=64
command=decode session_id=stage-0 stage_command=source_decode step=1
command=complete session_id=stage-0
command=abort session_id=stage-0
command=shutdown
```

This proves the long-lived process transport and in-memory session table that
the CMesh daemon will use next. The default prepare path still returns
`blocked_missing_native_prepare_hooks` so existing guarded lifecycle tests keep
their old behavior. When `native_prepare=1` is passed, the resident process
loads selected stage tensors from GGUF, creates a `llama_context`, and reports
`resident_model_context_ready_missing_decode_hooks`; decode still returns
`blocked_missing_native_decode_hooks` until per-stage `llama_decode` dispatch
and KV token-loop ownership are wired.

`scripts/llamacpp-stage-pipeline-e2e-smoke.sh` can run the first local real
stage pipeline proof with a GGUF model:

```bash
CMESH_GGUF_MODEL_PATH=/path/to/qwen.gguf \
scripts/llamacpp-stage-pipeline-e2e-smoke.sh
```

For repeatable local development without a pre-downloaded model:

```bash
CMESH_DOWNLOAD_GGUF_FIXTURE=1 \
scripts/llamacpp-stage-pipeline-e2e-smoke.sh
```

It splits the model into three layer ranges, runs `source-decode`, then
`decode`, then `terminal-decode`, and verifies that activation files and a
terminal token are produced. This is still local process orchestration, not
multi-machine network execution.

Local smoke with a real GGUF:

```bash
CMESH_GGUF_MODEL_PATH=/path/to/model.gguf \
CMESH_STAGE_START=0 \
CMESH_STAGE_END=15 \
scripts/llamacpp-stage-prepare-smoke.sh
```

The smoke validates the tensor manifest and selected tensor materialization
probe. Passing this smoke does not mean the model can execute as a sliced stage yet.
