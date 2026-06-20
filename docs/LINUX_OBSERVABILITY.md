# CMesh Linux Observability

This guide defines the minimum observability surface for Linux production
operators.

## Primary Endpoint

Use:

```sh
curl -fsS -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  http://MANAGER_HOST:18080/v1/observability
```

The response must expose:

- cluster counters
- worker summaries
- worker resource snapshots
- installed model inventory
- runtime readiness
- stage daemon endpoint and readiness
- distributed/CDIP job summaries
- RPC health counters
- recovery configuration
- blocker list

## Job Evidence

For every sliced run, save:

- `/v1/models/{model_id}/distributed-plan`
- `/v1/models/{model_id}/distributed-generate`
- `/v1/cdip/jobs/{job_id}/prepare`
- `/v1/cdip/jobs/{job_id}/decode-loop`
- `/v1/jobs/{job_id}`
- `/v1/observability`

The evidence bundle should include:

- coordinator/parent job ID
- stage job IDs
- assigned worker node IDs
- stage indexes and layer ranges
- model path or stage model paths
- runtime version
- stage daemon URL
- timing fields where available
- final output or failure cause

## Service Logs

Manager:

```sh
journalctl -u cmesh.service --no-pager -n 200
```

Worker:

```sh
journalctl -u cmesh-worker.service --no-pager -n 200
```

Stage daemon:

```sh
journalctl -u cmesh-stage-daemon.service --no-pager -n 200
```

## Healthy Production Signals

- manager `/health` responds
- `/v1/observability` is accessible only with operator token
- all expected workers are online
- every sliced worker reports `cmesh-stage-daemon`
- runtime readiness is true for the pinned llama.cpp stage runtime
- distributed plan is feasible and executable
- recent distributed job summaries include parent and stage jobs
- blocker list is empty for the supported model

## Validation

Run:

```sh
scripts/observability-smoke.sh
scripts/linux-production-reliability-smoke.sh
```

Keep the smoke output directory as evidence.
