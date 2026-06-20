# llama.cpp Stage Adapter Spike

Status: research spike, not a claim that CMesh runs real layer-sharded models yet.

Pinned llama.cpp reference inspected locally:

- Requested ref: `b9704`
- Resolved commit: `10786217e9d40c848ac0133cbe9c5f22a52421bb`
- Local checkout used for source inspection: `/tmp/cmesh-llama-b9704`

## Summary

CMesh cannot build a real layer-stage adapter on top of the public `llama-cli` interface alone.

The current public surface supports full-model inference and RPC backend offload. RPC can distribute GGML compute to remote devices, but it does not expose a stage API that loads only layer ranges, accepts upstream hidden-state tensors, emits downstream hidden-state tensors, or scopes KV cache to CMesh stage IDs.

The first real adapter therefore needs either:

1. a llama.cpp fork/patch with C ABI hooks for staged execution, or
2. a dedicated adapter binary built inside the llama.cpp tree that can use internal headers/classes.

The second option is less invasive for upstreaming because the first implementation can live as an experimental tool, but it still depends on internal llama.cpp graph/model/KV types.

## Source Findings

### Model Loading

Relevant files:

- `src/llama.cpp`
- `src/llama-model.cpp`
- `src/llama-model-loader.cpp`
- `src/models/*.cpp`

Key observations:

- `src/llama.cpp:330` calls `model->load_tensors(ml)`.
- `src/llama-model.cpp:1209` defines `llama_model_base::load_tensors`.
- `src/llama-model.cpp:1271-1296` assigns layers to CPU/GPU/backend devices using offload split logic, not CMesh pipeline stage ranges.
- `src/llama-model.cpp:1312-1315` resizes `layers` to `n_layer_all` and then calls the architecture-specific `load_arch_tensors`.
- `src/llama-model.cpp:1470` calls `ml.done_getting_tensors()` without `partial=true`, so ordinary model load expects the architecture loader to account for the full model tensor set.
- `src/llama-model.cpp:1607-1609` loads all tensor data for every created context via `ml.load_all_data`.
- `src/llama-model-loader.cpp:1047-1287` creates tensor metadata and selects the buffer type based on input/output/repeating layer classification.
- `src/llama-model-loader.cpp:1317-1327` has a `partial` argument in `done_getting_tensors`, but this is for loader accounting and sibling-model cases; it is not a public staged execution API.
- `src/llama-model-loader.cpp:1385-1406` loads a single tensor's data and `src/llama-model-loader.cpp:1408` loads all tensors in a context.

Architecture examples:

- `src/models/qwen2.cpp:18-50` creates token/output tensors and every repeating layer tensor for Qwen2.
- `src/models/gemma3.cpp:42-79` does the same for Gemma3.

Conclusion: model loading can be patched to materialize only a layer range plus required boundary tensors, but there is no CLI or public C API to request "load layers 12..23 as a standalone stage".

### Graph Execution And Hidden State

Relevant files:

- `src/llama-graph.h`
- `src/llama-graph.cpp`
- `src/models/qwen2.cpp`
- `src/models/gemma3.cpp`
- architecture-specific `src/models/*.cpp`

Key observations:

- `src/llama-graph.cpp:1833-1920` builds input embeddings. It already has a vector-embedding path through `inp->embd`, but that is the model input embedding path, not an arbitrary mid-layer hidden-state import.
- `src/models/qwen2.cpp:64-137` builds the Qwen2 graph as a loop over `for (int il = 0; il < n_layer; ++il)`.
- In Qwen2, `inpL` is the layer input and `cur` is the working hidden state. `src/models/qwen2.cpp:134-136` assigns `inpL = cur` after `cb(cur, "l_out", il)`.
- `src/models/gemma3.cpp:87-199` follows the same pattern with model-specific norms, attention, and FFN logic.
- `src/llama-graph.cpp:2066-2199` builds the attention body.
- `src/llama-graph.cpp:2311-2360` is the KV-cache attention path: it writes K/V to cache and reads cache tensors back for attention.

Conclusion: the hidden-state boundary exists conceptually at `l_out` / `inpL = cur`, but it is internal to each architecture graph constructor. A real stage adapter needs hooks to:

- replace the first stage layer input with token embeddings;
- replace a middle/terminal stage layer input with imported hidden-state tensor data;
- stop graph construction at `layer_end`;
- export `l_out` at the end of a non-terminal stage;
- run final norm/lm_head only on the terminal stage.

This must be patched in architecture graph builders or factored into a common staged graph path.

### KV Cache

Relevant files:

- `src/llama-kv-cache.h`
- `src/llama-kv-cache.cpp`
- `src/llama-memory.h`

Key observations:

- `src/llama-kv-cache.cpp:80-117` constructs cache state against `hparams.n_layer_all`.
- `src/llama-kv-cache.cpp:179-249` creates per-layer K/V cache tensors and has a `filter` callback that can skip layers internally.
- `src/llama-kv-cache.cpp:1239-1289` returns per-layer K/V views using `map_layer_ids.at(il)`.
- `src/llama-kv-cache.cpp:1291-1336` writes current K/V into the cache for a specific `il`.
- `src/llama-kv-cache.cpp:1953-2055` serializes/deserializes KV cache state.

Conclusion: KV cache is already per layer internally, but stage isolation still needs an explicit CMesh `kv_cache_key` mapping to llama.cpp seq/cache state, plus layer-range cache ownership. For non-terminal stages, the KV for only those layers should live on that stage. For terminal stages, logits require the final hidden state and terminal-stage KV.

### RPC Backend

Relevant files:

- `tools/rpc/README.md`
- `tools/rpc/rpc-server.cpp`
- `src/llama.cpp`
- `tools/llama-bench/llama-bench.cpp`

Key observations:

- `tools/rpc/README.md` describes `rpc-server` as exposing GGML devices to an RPC backend.
- `src/llama.cpp:238-245` inserts RPC devices into the model device list.
- `tools/llama-bench/llama-bench.cpp:175-192` registers RPC servers with the backend registry.

Conclusion: current CMesh distributed RPC baseline is real distributed compute, but it is not layer-stage sharding in the CDIP sense. It is a useful baseline and benchmark target, not the final protocol implementation.

## Can We Avoid A Fork?

Not for real layer-stage execution.

Without a fork or internal adapter binary, `llama-cli` cannot:

- load only a contiguous layer range as a named stage;
- import a tensor as the hidden state before layer `N`;
- export a tensor after layer `M`;
- route KV cache by CMesh `kv_cache_key`;
- report per-stage tensor shape/dtype/checksum;
- synchronize stage boundaries over CDIP.

The current no-fork path remains:

- full model local inference;
- llama.cpp RPC backend distribution;
- CDIP mock lifecycle with deterministic tensor envelopes.

## Files To Patch Or Wrap

Minimum llama.cpp integration targets:

- `include/llama.h`
  - Add experimental staged execution C API, or keep the API private to an experimental adapter tool.
- `src/llama-model.cpp`
  - Accept stage load options: `stage_layer_start`, `stage_layer_end`, `stage_role`.
  - Allow partial tensor accounting for stage materialization while still loading required embedding/output tensors based on role.
- `src/llama-model-loader.cpp`
  - Expose safe range-aware tensor creation/loading helpers, or allow selected tensors without treating the rest as an error.
- `src/models/qwen2.cpp`, `src/models/gemma3.cpp`, and later other target architectures
  - Split graph build into `build_layers(start,end,input)` and expose `l_out`.
  - Import upstream hidden state for non-first stages.
  - Export downstream hidden state for non-terminal stages.
  - Run output norm/lm_head only on terminal stages.
- `src/llama-graph.h` and `src/llama-graph.cpp`
  - Add generic hidden-state input builder.
  - Add hidden-state output capture metadata.
  - Keep shape/dtype introspection stable enough for CDIP envelopes.
- `src/llama-kv-cache.h` and `src/llama-kv-cache.cpp`
  - Scope cache state by stage layer range and CMesh `kv_cache_key`.
  - Expose cache state lifecycle hooks for prepare/prefill/decode/complete/abort.
- New tool, recommended first:
  - `tools/cmesh-stage-runner/`
  - Reads CDIP JSON command.
  - Links against llama.cpp internals.
  - Emits CMesh `TensorEnvelope` plus payload bytes.

## Minimum Adapter API

CMesh runtime boundary:

```go
type TensorEnvelope struct {
  dtype string
  shape []int
  byte_count int
  checksum string
  sequence uint64
  stage_index int
  kv_cache_key string
}
```

Operations:

### prepare_stage

Input:

- model path / model id
- architecture
- stage index
- layer_start
- layer_end
- upstream/downstream stage IDs
- role: first, middle, terminal, single
- runtime options

Output:

- stage prepared status
- loaded bytes
- supported tensor dtype(s)
- expected hidden-state shape
- `kv_cache_key`

### prefill_stage

Input:

- prompt tokens for first stage, or upstream hidden-state for middle/terminal stage
- `kv_cache_key`
- sequence id

Output:

- tensor envelope for downstream stage, or logits on terminal stage
- timing
- cache state status

### decode_stage

Input:

- new token or upstream hidden-state tensor
- `kv_cache_key`
- sequence id

Output:

- downstream tensor envelope, or terminal token/logits
- timing
- cache state status

### complete_stage

Input:

- parent job id
- stage job id
- `kv_cache_key`

Output:

- flushed/closed stage state
- optional cache retention decision

### abort_stage

Input:

- parent job id
- stage job id
- `kv_cache_key`
- reason

Output:

- released transient state

## Tensor Envelope

CMesh envelope fields:

```json
{
  "protocol": "cdip.tensor-envelope-v1",
  "dtype": "f16",
  "shape": [1, 4, 8],
  "byte_count": 64,
  "checksum": "sha256:<hex>",
  "sequence": 1,
  "stage_index": 0,
  "kv_cache_key": "job-123/seq-0",
  "encoding": "raw",
  "parent_job_id": "job-parent",
  "stage_job_id": "job-stage-0",
  "upstream_stage_job_id": "job-stage-0",
  "downstream_stage_job_id": "job-stage-1",
  "downstream_node_id": "node-b",
  "timing_ms": 12
}
```

Notes:

- `byte_count` is the raw payload byte length.
- `checksum` should be computed over raw payload bytes.
- `shape` is model/runtime specific and must be validated by the downstream stage before decode.
- `kv_cache_key` is required once real llama.cpp hooks exist; it may be empty in current mock smoke tests.

## Risks

### Latency

- Network transfer happens once per generated token for every stage boundary.
- Hidden-state payload size is roughly `batch * tokens * hidden_size * dtype_bytes`; for decode this is much smaller than prefill but still frequent.
- LAN latency may dominate small models. WAN stage boundaries will likely be unusable for token-by-token decode unless batching/speculative execution is added.

### Memory

- Each stage needs its layer weights plus per-layer KV cache for active sequences.
- First stage may need embedding tensors.
- Terminal stage needs final norm/lm_head tensors.
- Middle stages need imported hidden-state buffers and output buffers.

### Correctness

- Architecture-specific graph differences matter. Qwen2, Gemma3, DeepSeek, MoE, SWA, MLA, and recurrent models cannot all share a naive layer loop.
- KV cache state must match sequence, position, stage range, and model revision.
- Quantized tensors and GGML backend placement may produce dtype/layout differences that must be represented in the envelope.
- Any mismatch in shape/dtype/position will produce silent bad generations unless validated aggressively.

## Next Implementation Step

Keep the current CMesh guardrail:

> CDIP stage lifecycle and tensor envelopes are protocol scaffolding. Real layer sharding requires llama.cpp stage hooks.

CMesh now carries a reproducible scaffold for that branch:

- CMesh-owned source: `integrations/llamacpp/cmesh-stage-runner/`
- Worktree helper: `scripts/prepare-llamacpp-stage-runner-worktree.sh`
- Pinned ref used by default: `10786217e9d40c848ac0133cbe9c5f22a52421bb`

The helper clones/checks out llama.cpp, copies the CMesh tool into `tools/cmesh-stage-runner`, adds it to `tools/CMakeLists.txt`, builds the `cmesh-stage-runner` target, and runs `--probe`.

Current probe output is intentionally blocked when no model is provided:

```json
{
  "kind": "cmesh.llamacpp_stage_runner_probe",
  "status": "blocked",
  "runtime": "llama.cpp",
  "implemented_hooks": [
    "selected tensor materialization plan",
    "Qwen2 first-stage hidden-state output tensor marker",
    "Qwen2 middle-stage hidden-state graph input via ubatch.embd",
    "file-based activation payload to llama_batch.embd decode bridge",
    "local hidden-state output extraction to f32 activation file"
  ],
  "missing_hooks": [
    "CDIP relay activation frame to stage-runner decode bridge",
    "stage-scoped KV cache key",
    "terminal stage logits export",
    "remote stage decode loop"
  ],
  "guardrail": "not real layer sharding yet"
}
```

The `prepare` command has a first real metadata mode:

```bash
cmesh-stage-runner \
  --command prepare \
  --model /path/to/model.gguf \
  --stage-start 0 \
  --stage-end 15 \
  --stage-index 0
```

That path loads the GGUF through public llama.cpp APIs and returns model/layer metadata plus an expected hidden-state shape template.

It also opens the GGUF metadata with `gguf_init_from_file(... no_alloc=true)` and emits a `tensor_manifest` for the selected stage range:

- `blk.N.*` tensors where `N` is inside `[stage_start, stage_end]`;
- first-stage boundary tensors such as `token_embd.*`;
- terminal-stage boundary tensors such as `output_norm.*` and `output.*`;
- selected tensor count and selected bytes;
- a small tensor name/type/byte sample for auditability;
- a full machine-readable selected tensor allowlist when `--emit-tensor-list`
  is provided.

This is a metadata-only load plan. It is useful because CMesh can now explain
which tensors belong to a future stage before the llama.cpp fork supports
materializing only those tensors. It still returns `"executable": false`
because it has not loaded only the selected layers and cannot import/export
mid-layer hidden state yet.

With the CMesh llama.cpp patch applied, `prepare` can also run:

```bash
cmesh-stage-runner \
  --command prepare \
  --model /path/to/model.gguf \
  --stage-start 0 \
  --stage-end 15 \
  --stage-index 0 \
  --emit-tensor-list \
  --materialize-selected-tensors
```

That path feeds the selected tensor allowlist back into
`llama_model_params.cmesh_stage_tensor_allowlist` and checks whether patched
llama.cpp can materialize only that tensor set. This is the first real selected
tensor load probe. It is still not stage execution: the graph still needs to be
cut to the stage range and hidden-state import/export still need to be patched.

The patch now also contains the first Qwen2 graph range plumbing:

- experimental `llama_model_params.cmesh_stage_layer_start`;
- experimental `llama_model_params.cmesh_stage_layer_end`;
- public model getters for stage range and role;
- Qwen2 first-stage graph range support for layer `0..N`;
- non-terminal Qwen2 stage output through `res->t_embd` and callback label
  `cmesh_stage_hidden_out`.

Middle/terminal Qwen2 stages still abort because hidden-state import is not
implemented yet. This is intentional: stage 0 can be isolated first, then stage
1+ can be added once CDIP tensor import exists inside llama.cpp.

## Selected Tensor Load Plumbing Patch

CMesh now carries a first llama.cpp core patch artifact:

```text
integrations/llamacpp/patches/0001-cmesh-stage-selected-tensor-load-plumbing.patch
```

The patch is intentionally narrow:

- adds `llama_model_loader::cmesh_stage_tensor_allowlist`;
- adds `llama_model_loader::cmesh_stage_partial_load`;
- adds experimental `llama_model_params.cmesh_stage_tensor_allowlist`;
- adds experimental `llama_model_params.cmesh_stage_tensor_allowlist_count`;
- adds experimental `llama_model_params.cmesh_stage_layer_start`;
- adds experimental `llama_model_params.cmesh_stage_layer_end`;
- adds experimental `llama_model_params.cmesh_stage_partial_load`;
- makes `create_tensor(...)` skip tensors outside the allowlist;
- switches `llama_model_base::load_tensors(...)` to call
  `done_getting_tensors(ml.cmesh_stage_partial_load)`.
- makes Qwen2 first-stage graph construction stop at the configured layer range
  and expose hidden-state output instead of logits for non-terminal stages.

Repeatable verification:

```bash
scripts/verify-llamacpp-stage-loader-patch.sh
```

This verification applies the patch to the pinned llama.cpp ref and builds the
`llama` target. Passing it means the loader plumbing compiles. It still does not
mean a real stage can execute, because the adapter still needs a way to inject
the selected tensor list into `llama_model_loader`, construct Qwen2/Gemma stage
graphs, import hidden state, export hidden state, and scope KV cache by stage.

Repeatable local validation with a real GGUF:

```bash
CMESH_GGUF_MODEL_PATH=/path/to/model.gguf \
CMESH_STAGE_START=0 \
CMESH_STAGE_END=15 \
scripts/llamacpp-stage-prepare-smoke.sh
```

This smoke must pass before replacing metadata-only prepare with the first real Qwen2 stage hook.

Next branch should replace the metadata-only prepare with real `prepare/prefill/decode/complete/abort` hooks for one target architecture first, preferably Qwen2/Qwen2.5, because its layer graph is a straightforward transformer loop in `src/models/qwen2.cpp`.
