# Development

## Run A Local Test Cluster

Terminal 1:

```sh
go run ./cmd/cmesh manager start
```

Terminal 2:

```sh
go run ./cmd/cmesh worker run \
  --name local-dev-worker \
  --cpu 4 \
  --memory-gb 5 \
  --disk-gb 50 \
  --benchmark
```

Open:

```text
http://localhost:8080
```

Expected result:

- manager health is available at `/health`;
- dashboard shows one online worker;
- cluster summary includes allowed CPU, memory, and storage;
- cluster summary includes benchmark score after the first run;
- worker sends heartbeat updates until stopped.

Run benchmarks without keeping a worker process alive:

```sh
go run ./cmd/cmesh worker benchmark
```

Submit benchmark results for an already registered node:

```sh
go run ./cmd/cmesh worker benchmark --node-id <node-id>
```

## Run A Multi-Worker Local Test

Terminal 1:

```sh
go run ./cmd/cmesh manager start
```

Terminal 2:

```sh
go run ./cmd/cmesh dev local-cluster --workers 3
```

This registers several local test workers with different resource limits and submits benchmark results for each one. The workers are one-shot registrations, so they are useful for a quick dashboard aggregation and capacity-growth snapshot. They will become offline after the heartbeat timeout. For live liveness testing, use separate long-running `worker run` processes.

Expected dashboard signals:

- `Workers online` increases to the requested count;
- allowed CPU, memory, and storage increase;
- benchmark score increases after each worker benchmark is submitted.

## Test

```sh
go test ./...
```
