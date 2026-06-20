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

Release-track changes should also run the relevant smoke:

```sh
scripts/linux-production-docs-smoke.sh
scripts/linux-production-security-doc-smoke.sh
scripts/linux-stable-release-smoke.sh
```

Do not commit generated release artifacts from `dist/`, private signing keys,
local AWS evidence, or model weight files.

## Pull Requests

Good pull requests include:

- a clear problem statement;
- focused code changes;
- tests for changed behavior when practical;
- documentation updates for API or architecture changes.

## Release Claims

Keep public claims aligned with the release scope in
`docs/RELEASE_SCOPE.md`. Do not claim Windows/macOS production support,
arbitrary model slicing, public untrusted worker markets, or GPU production
support until those paths have their own evidence and milestones.
