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
    }
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
