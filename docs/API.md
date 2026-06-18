# API

CMesh manager exposes an HTTP API for early development.

## Health

```http
GET /health
```

Returns manager health and bootstrap mode.

## Cluster Summary

```http
GET /v1/cluster
```

Returns aggregate worker counts and resource totals.

## Nodes

```http
GET /v1/nodes
```

Returns registered manager and worker nodes.

## Benchmarks

```http
GET /v1/benchmarks
```

Returns latest benchmark results grouped by node.

```http
POST /v1/benchmarks
Content-Type: application/json
```

Example:

```json
{
  "node_id": "node-abc123",
  "kind": "cpu",
  "score": 128.4,
  "unit": "million_ops_per_second",
  "duration": 750000000,
  "metadata": {
    "threads": "8"
  }
}
```

## Worker Join

```http
POST /v1/workers/join
Content-Type: application/json
```

Example:

```json
{
  "node_name": "local-dev-worker",
  "role": "worker",
  "join_token": "replace-me",
  "resources": {
    "cpu": {
      "cores_total": 16,
      "cores_allowed": 4
    },
    "memory": {
      "total_bytes": 0,
      "allowed_bytes": 5368709120
    },
    "gpu": [],
    "storage": {
      "total_bytes": 0,
      "allowed_bytes": 53687091200,
      "free_bytes": 0
    },
    "models": [],
    "runtimes": [
      {
        "name": "llama.cpp",
        "ready": false,
        "platform": "darwin/arm64",
        "source": "cmesh-runtime-cache",
        "error": "runtime is not installed"
      }
    ]
  }
}
```

## Worker Heartbeat

```http
POST /v1/workers/heartbeat
Content-Type: application/json
```

Workers use this endpoint to refresh liveness and resource state after joining.

The manager currently marks workers offline when no heartbeat has been observed for the configured timeout window.

Heartbeat resources may include:

- `models`: model files actually present in the worker cache. The manager treats this as the source of truth for installed model inventory.
- `runtimes`: local AI runtime state such as `llama.cpp` readiness, version, platform, source, binary path, and error.

## Jobs

```http
POST /v1/jobs
Content-Type: application/json
```

Example:

```json
{
  "type": "echo",
  "input": "hello cluster",
  "requested_by": "developer",
  "assigned_to": "node-abc123",
  "requirements": {
    "cpu_cores": 2,
    "memory_bytes": 2147483648,
    "gpu_required": false,
    "vram_bytes": 0
  }
}
```

`assigned_to` and `requirements` are optional. When `assigned_to` is omitted, the manager schedules the job to the best currently online worker that satisfies the requested CPU, memory, disk, GPU, and VRAM limits. When `assigned_to` is present, the manager schedules the job only if that worker is online and capable. If no capable worker exists, the job stays `queued` with `last_failure` set to `waiting for capable worker`.

Jobs include retry metadata:

- `attempts`: number of times the job has been assigned to a worker.
- `max_attempts`: maximum assignment attempts before terminal failure.
- `last_failure`: latest placement or worker failure reason.

When a worker reports an execution error, the manager uses the same retry policy: it reschedules to another capable worker when attempts remain, queues the job when no capable worker is available, or marks it failed after `max_attempts`.

```http
GET /v1/jobs
GET /v1/jobs/{job_id}
```

Operators can cancel queued, scheduled, or running jobs:

```http
POST /v1/jobs/{job_id}/cancel
```

Workers poll assigned jobs:

```http
GET /v1/workers/{node_id}/jobs/next
```

Workers complete jobs:

```http
POST /v1/jobs/{job_id}/complete
Content-Type: application/json
```

Example:

```json
{
  "node_id": "node-abc123",
  "result": "hello cluster"
}
```

## CDIP Activation Relay

The first distributed-runtime transport is manager-relayed. It is intended for correctness and adapter development before direct worker-to-worker transport exists.

Push an activation frame:

```http
POST /v1/cdip/activations/{parent_job_id}/{stage_job_id}/frames
Content-Type: application/json
```

Example body:

```json
{
  "header": {
    "protocol": "cdip",
    "version": "0.1",
    "type": "activation.chunk",
    "parent_job_id": "job-parent",
    "stage_job_id": "job-stage-0",
    "sequence": 1,
    "content_type": "application/vnd.cmesh.activation+binary",
    "encoding": "raw",
    "shape": [1, 1, 4],
    "dtype": "f16",
    "payload_bytes": 4
  },
  "payload": "CQgHBg=="
}
```

Read the next activation frame:

```http
GET /v1/cdip/activations/{parent_job_id}/{stage_job_id}/frames?timeout_ms=250
```

Returns `200` with the frame when one is available, or `204 No Content` when the relay queue is empty before the timeout expires.

## Cluster Benchmarks

```http
POST /v1/cluster-benchmarks
Content-Type: application/json
```

Starts one `compute.matrix_multiply` job on each currently online capable worker and returns an aggregate run summary.

Example:

```json
{
  "size": 512,
  "iterations": 6,
  "requested_by": "dashboard"
}
```

```http
GET /v1/cluster-benchmarks
```

Returns recent benchmark runs reconstructed from jobs. Each summary includes worker count, completed/failed/active counts, workload size, and total GFLOPS from successful jobs.

## Models

```http
GET /v1/models
```

Returns catalog entries with current cluster capability, installed inventory, and runtime-ready inventory.

- `installed_on`: online workers that report the model file in heartbeat `resources.models`.
- `generatable_on`: installed workers whose required runtime, such as `llama.cpp`, is also reported ready in heartbeat `resources.runtimes`.

A model can be installed but not generatable when the model file exists but the runtime is missing, outdated, or still installing.

```http
POST /v1/models/{model_id}/install
Content-Type: application/json
```

Optional body:

```json
{
  "node_id": "node-abc123"
}
```

When `node_id` is omitted, the manager schedules install on an eligible online worker. Eligibility checks RAM, allowed disk, actual free disk, VRAM, and available worker job slots. If no worker is eligible, the API returns `409 Conflict` with actionable reasons.

```http
POST /v1/models/{model_id}/delete
Content-Type: application/json
```

Example:

```json
{
  "node_id": "node-abc123"
}
```

Delete removes the worker model directory and returns `freed_bytes` in the job result when the worker completes the operation. Successful delete also clears model-scoped memory and conversations in the manager and adds `deleted_memories` plus `deleted_conversations` to the job result.

```http
POST /v1/models/{model_id}/generate
Content-Type: application/json
```

Example:

```json
{
  "node_id": "node-abc123",
  "conversation_id": "conv-abc123",
  "system_prompt": "Answer concisely.",
  "prompt": "Привіт",
  "max_tokens": 512,
  "temperature": "0.7"
}
```

Generation uses model-family prompt adapters, model-scoped memory, and conversation history. Responses run on the selected worker, not an external API.

The selected worker must be listed in `generatable_on` for that model. If the model is installed but the runtime is not ready, the API returns `409 Conflict` before creating a generate job.

## Conversations

```http
GET /v1/conversations
GET /v1/conversations/{conversation_id}
DELETE /v1/conversations/{conversation_id}
```

Conversations persist chat history and selected model context in the manager. Deleting a conversation does not delete model memory.

## Memories

```http
GET /v1/memories?model_id={model_id}
POST /v1/memories
POST /v1/memories/{memory_id}
DELETE /v1/memories/{memory_id}
DELETE /v1/memories?model_id={model_id}
GET /v1/memories/preview?model_id={model_id}
```

Manual memory example:

```json
{
  "model_id": "qwen2.5-0.5b-instruct-q4-k-m",
  "key": "user.name",
  "value": "Сергій",
  "source": "manual"
}
```

Memory is scoped to a model and injected into the effective system prompt before chat messages.
