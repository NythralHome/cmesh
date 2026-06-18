# Distributed Runtime Feasibility

CMesh already has the CDIP control plane for distributed inference planning, stage jobs, lifecycle transitions, and activation frame envelopes. That is not the same as real cross-machine tensor execution.

This document defines the runtime gap we must close before claiming that CMesh can run one model across multiple machines.

## Current Conclusion

The distributed direction is valid, but the next milestone is a runtime adapter spike, not UI polish.

CMesh should keep CDIP as the protocol contract and implement real distributed inference behind it in stages:

1. Manager-relayed activation frame transport.
2. Worker-side HTTP activation client for sending and receiving frames through the manager relay. The initial code contract is `transport.HTTPActivationTransport`.
3. Runtime adapter interface for stage execution. The initial code contract is `runtimes.DistributedStageRuntime`.
4. llama.cpp stage feasibility prototype.
5. Logical-to-physical model shard materialization.
6. LAN-only latency and correctness benchmark.

Until these exist, distributed plans must remain `executable_now: false`.

## What llama.cpp Supports Today

llama.cpp has official multi-GPU support for one host where multiple GPUs are visible to the same runtime process.

The official multi-GPU guide documents:

- `layer` split mode as the default pipeline-parallel mode, where each GPU owns a contiguous slice of layers.
- `tensor` split mode as experimental tensor parallelism.
- interconnect speed as a major performance bottleneck, especially for tensor parallelism.

Source: [llama.cpp multi-GPU guide](https://github.com/ggml-org/llama.cpp/blob/master/docs/multi-gpu.md).

This maps well to CDIP's `pipeline_layers` plan shape, because CDIP also assigns contiguous layer ranges to stages.

## What CMesh Needs

CMesh workers are separate machines or independent processes. A normal `llama-cli` invocation runs the whole model inside one runtime process and does not expose a stable external API for:

- loading only a layer range as an independent stage;
- accepting hidden-state activations from an upstream worker;
- returning hidden-state activations to a downstream worker;
- keeping KV/cache state scoped to a distributed conversation;
- emitting partial tokens from the final stage while upstream stages continue decoding.

That means current full-model generation remains valid, but real distributed generation needs a runtime adapter.

## Runtime Adapter Contract

The first adapter is intentionally narrow:

```text
PrepareStage(model, shard) -> stage_ready
Prefill(stage, input_tokens, upstream_activation?) -> downstream_activation
Decode(stage, step, token?, upstream_activation?) -> downstream_activation | token_delta
Complete(stage) -> metrics
Abort(stage, reason) -> cleanup
```

The adapter can initially run behind one implementation:

- `logical-stage`, which validates CDIP stage contracts and reports stage readiness without real tensor execution.
- `llama.cpp-stage-experimental`, which should become the first real layer-stage prototype.

If llama.cpp cannot expose the needed hooks cleanly, the adapter boundary lets us test another runtime without replacing CDIP.

## Activation Transport

The first real transport should be manager-relayed, not direct worker-to-worker.

Reason:

- workers may be behind NAT;
- the manager already authenticates the cluster;
- it is easier to debug and benchmark;
- we can later add direct worker links as an optimization.

Transport requirements:

- stream ID per distributed generation;
- ordered frame sequence;
- binary payload support;
- per-stage acknowledgements;
- cancellation;
- frame size and timeout limits;
- metrics for bytes, latency, and retries.

## Shard Materialization

CDIP v0.1 uses `logical_layers`:

- every worker may still have the full GGUF model;
- the shard manifest tells the adapter which layer range it owns;
- no physical GGUF slicing is required for the first correctness prototype.

Physical shard files can come later if we need lower disk usage or faster installs.

## Benchmark Gate

The first real distributed benchmark must answer three questions:

- Does output match single-worker output closely enough for deterministic test prompts?
- How much latency is added per output token per network hop?
- Does the system recover cleanly when a stage worker disappears?

Minimum benchmark matrix:

- 2 workers on one LAN;
- one small model that already runs locally;
- prompts with fixed seed and short output;
- parent job and all stage jobs visible in the manager;
- activation bytes and per-stage timing recorded.

## Product Claim Boundary

Allowed claim now:

> CMesh has CDIP, a protocol and control plane for planning and coordinating distributed model execution.

Not allowed yet:

> CMesh can already run one model split across arbitrary machines.

That claim becomes true only after the runtime adapter executes real layer stages and transfers real activations across workers.
