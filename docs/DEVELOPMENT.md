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

## Test

```sh
go test ./...
```
