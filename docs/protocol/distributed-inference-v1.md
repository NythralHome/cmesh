# CMesh Distributed Inference Protocol v1

Status: baseline

CMesh Distributed Inference Protocol v1 defines the first executable distributed model path in CMesh.

The current executable mode is `cmesh.distributed-rpc`: CMesh orchestrates cluster membership, placement, readiness, jobs, and traces, while `llama.cpp` performs model execution with RPC backend workers.

This is real multi-machine inference through a coordinator and one or more RPC backends. It is not yet layer sharding. Native layer-stage execution remains the CDIP path described in `docs/CDIP.md`.

## Roles

### Manager

The manager owns cluster state:

- worker registration and heartbeat state;
- model catalog and installed model inventory;
- RPC backend discovery and health checks;
- coordinator selection;
- distributed execution plan creation;
- job lifecycle and execution trace persistence.

### Coordinator Worker

The coordinator worker owns the model file for the request. It runs `llama-cli` with a selected RPC backend list:

```text
llama-cli -m <model.gguf> --rpc <backend-a:50052,backend-b:50052>
```

The coordinator is the worker assigned to the `model.generate.distributed_rpc` job.

### RPC Backend Worker

An RPC backend worker runs `llama.cpp` `rpc-server` and advertises an endpoint through heartbeat runtime resources.

The manager only schedules endpoints that are:

- reported by an online worker;
- runtime-ready;
- RPC-ready;
- not the selected coordinator;
- not quarantined by repeated health failures;
- not currently busy with another job.

### Model Owner

The model owner is the coordinator worker for v1. The full model is installed on that worker. Backend workers contribute compute/memory through llama.cpp RPC but do not currently own independent model shards.

## Wire Contract

Every distributed RPC job input includes an execution plan:

```json
{
  "protocol": "cmesh.distributed-rpc",
  "protocol_version": 1,
  "plan_schema_version": 1,
  "mode": "llama.cpp-rpc",
  "model_id": "qwen2.5-0.5b-instruct-q4-k-m",
  "coordinator_node_id": "node-coordinator",
  "coordinator_node_name": "coordinator",
  "rpc_endpoints": ["10.0.0.10:50052", "10.0.0.11:50052"],
  "backends": [
    {
      "node_id": "node-backend-a",
      "node_name": "backend-a",
      "runtime": "llama.cpp",
      "runtime_version": "llama.cpp-b9704-linux-amd64-rpc",
      "endpoint": "10.0.0.10:50052",
      "health_status": "ready"
    }
  ],
  "health_checked": true,
  "planned_at": "2026-06-18T23:53:33Z"
}
```

Workers reject plans with an unsupported protocol, protocol version, schema version, model id, coordinator id, missing endpoints, or backend endpoints outside the plan endpoint list.

## Capability Negotiation

Workers advertise runtime capabilities in heartbeat `resources.runtimes`.

Required coordinator capability:

- `llama.cpp` runtime ready;
- model installed and generate-ready;
- runtime version compatible with selected backends.

Required backend capability:

- `llama.cpp` runtime ready;
- `llama.cpp-rpc` ready;
- endpoint reachable by the coordinator network;
- runtime version compatible with the coordinator when both sides report a version.

The current pinned Linux runtime is:

```text
llama.cpp-b9704-linux-amd64-rpc
```

## Job Lifecycle

1. Worker joins cluster and advertises resources.
2. RPC backend workers start `rpc-server` and heartbeat `rpc_runtimes`.
3. Manager refreshes RPC health.
4. Manager selects a coordinator where the model is installed and generatable.
5. Manager excludes the coordinator from backend endpoints.
6. Manager creates a `cmesh.distributed-rpc` execution plan.
7. Manager creates `model.generate.distributed_rpc`.
8. Coordinator worker validates the plan.
9. Coordinator runs `llama-cli --rpc <endpoints>`.
10. Worker returns an execution result trace.
11. Manager stores the completed job result.

## Execution Trace

Every successful `model.generate.distributed_rpc` result should include:

- protocol and schema versions;
- plan id;
- coordinator node id/name;
- backend node ids/names/endpoints;
- runtime and runtime version;
- model path and model bytes on the coordinator;
- `rpc_enabled`;
- endpoint count and endpoint list;
- timings:
  - model stat time;
  - runtime prepare time;
  - llama process time;
  - total worker execution time;
- output text;
- started and completed timestamps.

Example:

```json
{
  "kind": "model.generate.distributed_rpc",
  "model_id": "qwen2.5-0.5b-instruct-q4-k-m",
  "runtime": "llama.cpp",
  "runtime_version": "llama.cpp-b9704-linux-amd64-rpc",
  "coordinator_node_id": "node-coordinator",
  "rpc_enabled": true,
  "rpc_endpoint_count": 2,
  "rpc_endpoints": ["10.0.0.10:50052", "10.0.0.11:50052"],
  "model_path": "/var/lib/cmesh/cache/models/qwen/model.gguf",
  "model_bytes": 491400032,
  "timings": {
    "model_stat_ms": 1,
    "runtime_prepare_ms": 0,
    "llama_process_ms": 7204,
    "total_ms": 7205
  }
}
```

## Readiness Gate

Distributed RPC readiness is true only when:

- the model is installed on a coordinator worker;
- the coordinator can generate that model;
- at least two schedulable RPC backend endpoints exist outside the coordinator;
- endpoint health checks pass when requested;
- runtime versions are compatible when reported;
- the manager can create a valid execution plan.

## Benchmark Gate

The baseline benchmark compares:

- local coordinator-only generation;
- RPC with one backend;
- RPC with two backends;
- RPC with three backends when available.

The benchmark records job ids, endpoint count, runtime version, duration, model bytes, and output sample in an evidence directory.

## Claim Boundary

Allowed claim for v1:

> CMesh can orchestrate a real distributed llama.cpp RPC inference run across multiple machines and record a protocol trace for it.

Not allowed claim for v1:

> CMesh can split arbitrary model layers across arbitrary machines with native activation streaming.

That second claim requires CDIP layer-stage runtime support.
