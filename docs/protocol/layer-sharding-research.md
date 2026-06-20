# Layer Sharding Research

Status: research

This document defines the path from the current executable `cmesh.distributed-rpc` baseline to native CMesh layer sharding.

## Target

Run one model across multiple machines by assigning contiguous model layer ranges to workers and streaming hidden-state activations between them.

Target mode:

```text
cdip.pipeline_layers
```

The manager creates a stage graph:

```text
tokens -> stage 0 layers 0..N -> activation -> stage 1 layers N+1..M -> logits/tokens
```

## Why llama.cpp CLI Is Not Enough

Public `llama-cli` can run full-model inference and can use `--rpc` backends, but it does not expose stable external hooks for:

- loading only a specific layer range as a standalone stage;
- accepting upstream hidden-state tensors;
- emitting downstream hidden-state tensors;
- preserving per-stage KV cache across decode steps;
- returning final token deltas from only the terminal stage.

That means native layer sharding needs either:

1. a llama.cpp patch/adapter;
2. a dedicated runtime adapter built around llama.cpp internals;
3. another runtime with explicit pipeline/tensor parallel APIs.

## Proposed Protocol Shape

CMesh should keep CDIP as the protocol and implement runtime adapters behind it.

Required stage calls:

```text
PrepareStage(model, shard, cache_key) -> stage_ready
Prefill(stage, tokens | upstream_activation) -> downstream_activation | logits
Decode(stage, token | upstream_activation, step) -> downstream_activation | token_delta
Complete(stage) -> metrics
Abort(stage, reason) -> cleanup
```

## GGUF / Layer Split Strategy

Phase 1 should use logical sharding:

- every stage can have access to the original GGUF;
- shard manifest tells the adapter which layer range to execute;
- no physical GGUF rewrite is required;
- correctness is easier to verify.

Phase 2 can introduce physical sharding:

- split or materialize layer tensors into per-stage artifacts;
- keep tokenizer, embeddings, normalization, and output head rules explicit;
- store shard manifests with hashes and source model metadata;
- avoid downloading full model files to every worker.

Open research questions:

- Which tensors must be replicated on first/last stage?
- Can GGUF metadata represent partial layer artifacts cleanly enough?
- Do quantized tensors need special handling when moved into shard files?
- How should LoRA/adapters be applied across partial shards?

## Activation Transport

The first transport should be manager-relayed:

- NAT-friendly;
- easier to authenticate;
- easier to trace and benchmark;
- slower than direct worker links, but simpler for correctness.

Frame envelope:

```json
{
  "protocol": "cdip",
  "version": "0.1",
  "type": "activation.frame",
  "stream_id": "stream-abc",
  "parent_job_id": "job-parent",
  "stage_job_id": "job-stage-1",
  "sequence": 42,
  "dtype": "f16",
  "shape": [1, 2048, 4096],
  "payload_encoding": "raw"
}
```

Payload should move as binary bytes, not JSON.

## KV Cache

KV cache cannot be global in a naive way. Each stage needs its own cache scoped by:

- model id;
- conversation id;
- parent job id;
- stage id;
- decode step;
- batch/session key.

For pipeline layers, each stage owns KV cache for its assigned layers only.

Failure handling:

- if a stage dies, its KV cache is lost;
- v1 should abort the request rather than reconstructing cache;
- later versions can checkpoint per-stage cache for long sessions.

## Latency Model

For N stages across machines, per-token latency roughly becomes:

```text
latency_per_token =
  sum(stage_compute_ms)
  + sum(network_activation_transfer_ms)
  + coordinator_overhead_ms
```

The dangerous part is that activation transfer happens for prefill and every decode step.

A 10-machine pipeline over WAN is expected to be slow for interactive chat unless:

- each stage has enough compute to justify the network hop;
- links are low-latency and high-bandwidth;
- batching is used;
- activation frames are compact;
- stages are placed in the same region/LAN.

Expected practical path:

- LAN/VPC first;
- 2-3 stages first;
- large models only;
- short outputs for proof;
- benchmark every hop.

## llama.cpp Patch Spike

The first spike should answer:

1. Can llama.cpp load only a contiguous layer range?
2. Can we expose prefill/decode calls that accept and return hidden-state buffers?
3. Can per-layer KV cache be isolated by stage?
4. Can the final stage emit logits/token deltas without earlier layers present?
5. Can this be built as a small adapter without maintaining a permanent fork?

Suggested spike output:

- `cmesh-llama-stage-runner` binary;
- `prepare`, `prefill`, `decode`, `complete` subcommands or RPC API;
- one local two-process test before multi-machine;
- deterministic tiny model test;
- metrics for activation bytes and per-stage latency.

Current repository spike:

```sh
cmesh stage-runner prepare --input stage.json --mode logical
cmesh stage-runner prefill --parent-job job-parent --stage-job job-stage-0 --stage-index 0
cmesh stage-runner decode --parent-job job-parent --stage-job job-stage-0 --stage-index 0 --payload activation --downstream-node node-b --manager http://127.0.0.1:18080
cmesh stage-runner complete --parent-job job-parent --stage-job job-stage-0 --stage-index 0
cmesh stage-runner probe-llamacpp --llama-cli /path/to/llama-cli
cmesh stage-runner prepare --input stage.json --mode llama.cpp-stage --llama-cli /path/to/llama-cli
```

`logical` validates the CDIP stage contract and returns `cdip.stage_ready`.
`decode` can send activation frames through the manager HTTP activation relay.
`llama.cpp-stage` probes `llama-cli` but intentionally blocks prepare because
public `llama-cli` still lacks the required layer-range and activation hooks.

## Milestones

### L0: Current Baseline

`cmesh.distributed-rpc` runs today through llama.cpp RPC backend workers.

### L1: Logical Stage Contract

Workers validate shard manifests, create stage jobs, and exchange mock activation frames.

### L2: Local Stage Runtime

Two local stage processes execute a tiny model with real activation transfer.

### L3: LAN Stage Runtime

Two machines run real layer stages through manager-relayed activation frames.

### L4: Direct Worker Links

Workers negotiate direct links for activation frames while manager keeps control-plane authority.

### L5: Physical Shards

Workers install only the model layers they own.

## Success Criteria

Layer sharding can be called real only when:

- model layers are assigned to distinct workers;
- workers execute only their assigned layer ranges;
- hidden-state activations cross worker boundaries;
- KV cache is held per stage;
- final output is generated by the terminal stage;
- trace records per-stage compute, bytes, and network timing;
- correctness is compared against a single-worker baseline.
