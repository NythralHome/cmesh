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
  --disk-gb 50
```

Open:

```text
http://localhost:8080
```

Expected result:

- manager health is available at `/health`;
- dashboard shows one online worker;
- cluster summary includes allowed CPU, memory, and storage;
- worker sends heartbeat updates until stopped.

## Test

```sh
go test ./...
```

