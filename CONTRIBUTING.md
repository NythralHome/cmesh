# Contributing

CMesh is early-stage infrastructure. Contributions should favor clear design boundaries, testable behavior, and small reviewable changes.

## Development Principles

- Keep manager, worker, scheduler, storage, and consensus concerns separate.
- Avoid hidden single-node assumptions in shared code.
- Prefer explicit interfaces around cluster state and transport.
- Keep MVP behavior honest: benchmark and route workloads before claiming distributed model execution.
- Add documentation for architectural decisions that affect future compatibility.

## Local Workflow

```sh
go test ./...
go run ./cmd/cmesh --help
```

The web dashboard will live under `web/` once the frontend stack is introduced.

## Pull Requests

Good pull requests include:

- a clear problem statement;
- focused code changes;
- tests for changed behavior when practical;
- documentation updates for API or architecture changes.

