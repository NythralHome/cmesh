# CDIP: CMesh Distributed Inference Protocol

Status: draft v0.1

CDIP is the CMesh protocol for planning, placing, and coordinating distributed AI inference across heterogeneous worker nodes.

CDIP v0.1 is intentionally narrow. It does not claim production-grade cross-machine tensor parallelism yet. It defines the protocol surface that makes distributed execution implementable:

- capability negotiation
- model placement planning
- shard manifest exchange
- stage graph creation
- stage lifecycle messages
- activation transport frame envelopes
- failure, cancellation, and compatibility rules

The first implementation may use HTTP JSON for control messages and a mock activation transport. The protocol keeps wire-level message names, versions, states, and validation rules explicit so a future gRPC, QUIC, or binary transport can implement the same semantics.

## Goals

- Let workers advertise the resources and runtimes needed for AI inference.
- Let a manager build a distributed execution plan for a model.
- Represent one user request as a parent job plus ordered stage jobs.
- Define how stage workers prepare, exchange activations, stream decode steps, and stop.
- Keep the protocol versioned and testable.

## Non-Goals In v0.1

- No public permissionless marketplace.
- No Byzantine fault tolerance.
- No claim that every GGUF model can be split across arbitrary machines today.
- No stable binary activation tensor ABI yet.
- No automatic fine-tuning, training, or gradient exchange.

## Roles

### Manager

The manager owns cluster state, worker liveness, model catalog metadata, planning, job graph creation, and orchestration.

### Worker

A worker contributes bounded resources and executes jobs. In distributed mode, a worker executes one or more model stages.

### Stage

A stage is a contiguous model layer range assigned to a worker. Stage `0` receives prompt/input state. The final stage produces logits/tokens.

### Coordinator

The coordinator is the manager-side state machine that owns one distributed inference request. It creates and monitors stage jobs.

## Protocol Versioning

Every CDIP message MUST include:

```json
{
  "protocol": "cdip",
  "version": "0.1",
  "type": "stage.prepare"
}
```

Peers MUST reject messages where:

- `protocol != "cdip"`
- `version` is unsupported
- `type` is unknown for that version
- required fields for that message type are missing

Future versions should use feature flags rather than silently changing message semantics.

## Capability Negotiation

Workers advertise capabilities through cluster heartbeat resources and, later, an explicit CDIP `node.hello` message.

Minimum capability fields:

```json
{
  "protocol": "cdip",
  "version": "0.1",
  "type": "node.hello",
  "node_id": "node-a",
  "runtimes": [
    {
      "name": "llama.cpp",
      "ready": true,
      "features": ["full-model-generate"]
    }
  ],
  "resources": {
    "cpu_cores": 8,
    "memory_bytes": 17179869184,
    "disk_bytes": 53687091200,
    "vram_bytes": 0
  },
  "network": {
    "listen_endpoint": "https://node-a.example/cdip",
    "estimated_rtt_ms": 40
  }
}
```

Future runtime features:

- `full-model-generate`
- `pipeline-stage-prepare`
- `pipeline-prefill`
- `pipeline-decode`
- `activation-stream-v1`
- `physical-shard-materialization`

## Planning Modes

CDIP v0.1 defines these placement modes:

- `single_worker`: full model fits on one worker.
- `pipeline_layers`: contiguous layer ranges are assigned to multiple workers.
- `replicated`: same full model is installed on multiple workers for throughput.

Future modes:

- `tensor_parallel`
- `expert_parallel`
- `speculative_decode`

## Distributed Plan

A distributed plan MUST include:

- model id
- mode
- runtime
- total layers
- ordered stages
- per-stage node id
- per-stage layer range
- blockers and warnings
- executable flag

Example:

```json
{
  "protocol": "cdip",
  "version": "0.1",
  "type": "plan.proposal",
  "model_id": "qwen2.5-14b-instruct-q4-k-m",
  "mode": "pipeline_layers",
  "runtime": "llama.cpp",
  "executable_now": false,
  "stages": [
    {
      "index": 0,
      "node_id": "node-a",
      "layer_start": 0,
      "layer_end": 23
    },
    {
      "index": 1,
      "node_id": "node-b",
      "layer_start": 24,
      "layer_end": 47
    }
  ],
  "blockers": [
    "distributed tensor runtime adapter is not implemented yet"
  ]
}
```

Stage ranges MUST be contiguous and non-overlapping. Stage index order is execution order.

## Shard Manifest

A shard manifest turns a placement plan into the concrete model split contract that workers can prepare.

CDIP v0.1 starts with `logical_layers` materialization:

- the model is split by contiguous layer ranges
- each shard maps to exactly one stage
- the source artifact can still be the original model file
- the worker/runtime is responsible for loading only the assigned layer range when the runtime supports it

CDIP v0.1 does not yet implement physical GGUF file slicing. A future implementation can use `physical_artifact` materialization when the manager or an offline build step creates standalone shard artifacts.

Example:

```json
{
  "protocol": "cdip",
  "version": "0.1",
  "type": "shard.manifest",
  "model": {
    "model_id": "qwen2.5-14b-instruct-q4-k-m",
    "runtime": "llama.cpp",
    "repository": "Qwen/Qwen2.5-14B-Instruct-GGUF",
    "file": "qwen2.5-14b-instruct-q4_k_m.gguf",
    "quant": "Q4_K_M",
    "parameters": "14B"
  },
  "mode": "pipeline_layers",
  "total_layers": 48,
  "materialization": "logical_layers",
  "shards": [
    {
      "stage": {
        "index": 0,
        "node_id": "node-a",
        "layer_start": 0,
        "layer_end": 23
      },
      "runtime": "llama.cpp",
      "source_artifact": "https://huggingface.co/.../qwen.gguf",
      "target_artifact": "qwen2.5-14b-instruct-q4-k-m.stage-0.layers-0-23",
      "materialization": "logical_layers",
      "capabilities": ["pipeline-stage-prepare", "pipeline-prefill", "pipeline-decode", "activation-stream-v1"]
    },
    {
      "stage": {
        "index": 1,
        "node_id": "node-b",
        "layer_start": 24,
        "layer_end": 47
      },
      "runtime": "llama.cpp",
      "source_artifact": "https://huggingface.co/.../qwen.gguf",
      "target_artifact": "qwen2.5-14b-instruct-q4-k-m.stage-1.layers-24-47",
      "materialization": "logical_layers",
      "capabilities": ["pipeline-stage-prepare", "pipeline-prefill", "pipeline-decode", "activation-stream-v1"]
    }
  ],
  "warnings": [
    "logical layer split only; physical GGUF shard materialization is not implemented yet"
  ]
}
```

Shard manifest validation rules:

- `model.model_id`, `model.runtime`, `mode`, and `materialization` are required.
- `total_layers` MUST be positive.
- every shard MUST have a runtime and materialization mode.
- shard stages MUST be ordered by `stage.index`.
- shard layer ranges MUST be contiguous and non-overlapping.
- the first shard MUST start at layer `0`.
- the final shard MUST end at `total_layers - 1`.

## Job Graph

One distributed inference request maps to:

- one parent `model.generate.distributed` job
- one `model.generate.distributed.stage` job per stage

The parent job owns conversation input and output semantics. Stage jobs own layer ranges and worker assignment.

In CDIP v0.1, planned stage jobs are coordinator-driven. They can be prepared and advanced through the control plane, but real cross-machine tensor execution remains blocked until an activation transport and runtime adapter exist.

## Stage Lifecycle

Stage states:

```text
planned -> preparing -> ready -> prefill -> decode -> completed
                     \-> failed
                     \-> aborted
```

Rules:

- A stage MUST NOT enter `prefill` before all previous stages are `ready`.
- A stage MUST NOT enter `decode` before the coordinator starts a decode step.
- Any stage can enter `failed`.
- If one stage fails, the coordinator MUST abort all non-terminal stages for the same parent job.
- Cancellation MUST be idempotent.

## Control Messages

### `stage.prepare`

Sent by manager/coordinator to a worker before execution.

Required fields:

- `parent_job_id`
- `stage_job_id`
- `model_id`
- `stage.index`
- `stage.layer_start`
- `stage.layer_end`
- `upstream_node_id`
- `downstream_node_id`

### `stage.ready`

Sent by worker when model shard and runtime are ready.

### `stage.prefill`

Starts prompt prefill. Stage 0 receives the prompt tokens. Later stages receive activation frames from upstream.

### `stage.decode`

Starts or continues one decode step.

### `stage.complete`

Sent by final stage or coordinator when the request has completed.

### `stage.abort`

Cancels a stage.

## Activation Transport

CDIP separates control messages from activation frames.

Control messages can be JSON over HTTP in v0.1. Activation frames require streaming transport.

The first implemented relay is manager-mediated HTTP:

```http
POST /v1/cdip/activations/{parent_job_id}/{stage_job_id}/frames
GET /v1/cdip/activations/{parent_job_id}/{stage_job_id}/frames?timeout_ms=250
```

This endpoint is a relay, not the final high-performance transport. It exists so workers can exchange validated activation frame envelopes through the manager while the runtime adapter is being developed. A future direct worker transport can keep the same CDIP frame contract.

Workers authenticate to the relay with `X-CMesh-Node-ID`. The manager authorizes only the stage owner and the downstream stage worker for that stream. Operators can still inspect through normal operator auth.

Frame envelope:

```json
{
  "protocol": "cdip",
  "version": "0.1",
  "type": "activation.chunk",
  "parent_job_id": "job-parent",
  "stage_job_id": "job-stage-0",
  "sequence": 12,
  "content_type": "application/vnd.cmesh.activation+binary",
  "encoding": "raw",
  "shape": [1, 128, 4096],
  "dtype": "f16",
  "payload_bytes": 1048576,
  "checksum": "sha256:..."
}
```

The binary payload MAY be sent after the JSON envelope, depending on transport. The HTTP relay currently accepts JSON with `header` and base64-encoded `payload` fields.

v0.1 activation transport requirements:

- preserve frame order per stage edge
- expose sequence numbers
- support abort
- expose backpressure
- surface timeout and checksum errors

## Failure Semantics

Failures MUST include:

- machine-readable code
- human-readable message
- retryable flag
- node id when known
- stage index when known

Example codes:

- `runtime_missing`
- `model_shard_missing`
- `activation_timeout`
- `activation_checksum_failed`
- `worker_offline`
- `protocol_version_unsupported`
- `stage_order_invalid`

## Security Model

CDIP v0.1 assumes trusted or semi-trusted private clusters.

Required:

- manager-issued join token
- operator/admin token for control APIs
- TLS for internet-exposed managers/workers

Future:

- mTLS worker identity
- signed model shard manifests
- attestation
- worker reputation
- encrypted activation streams

## Conformance

A CDIP implementation should pass tests for:

- protocol/version validation
- message type validation
- stage range validation
- stage lifecycle transition validation
- job graph construction
- activation frame envelope validation

The reference implementation lives in `internal/cdip`.
