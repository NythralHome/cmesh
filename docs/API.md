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
  "requested_by": "developer"
}
```

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
