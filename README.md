# CMesh

CMesh is a decentralized-ready AI compute cluster for pooling heterogeneous machines into a private AI infrastructure.

The first release focuses on connecting worker nodes, measuring real available capacity, tracking CPU/GPU/RAM/storage, and routing AI workloads to the best available node. The project is designed so a single-manager development cluster can evolve into a replicated multi-manager cluster with consensus.

## Why CMesh Exists

Many teams and independent builders have useful compute spread across laptops, workstations, gaming PCs, lab machines, and small servers. Those resources are hard to discover, benchmark, compare, and use as one operational AI cluster.

CMesh aims to solve that by providing:

- worker onboarding with explicit resource limits;
- cluster-wide resource and benchmark visibility;
- decentralized-ready manager nodes;
- AI workload scheduling;
- model and artifact cache awareness;
- a web dashboard for operators;
- an API surface for automation and future integrations.

## What V1 Is

V1 is not a promise that ten weak machines automatically become one large GPU. The first target is a practical private cluster:

- connect machines as workers;
- report allowed CPU, memory, GPU, VRAM, and disk;
- run benchmarks to measure real capacity;
- show cluster state in a dashboard;
- submit simple AI jobs;
- schedule jobs to the best available worker.

Distributed execution of one large model across multiple machines is a later milestone and will require explicit runtime support and strong network assumptions.

## Architecture

CMesh uses two node roles:

- **Manager nodes** maintain cluster state, expose APIs, and run scheduling decisions. In development, one manager can run alone. In production, managers should replicate state through a consensus layer.
- **Worker nodes** contribute bounded compute and storage resources, send heartbeats, run benchmarks, cache AI artifacts, and execute assigned jobs.

Core design principle:

```text
single-manager bootstrap, multi-manager architecture
```

The codebase keeps consensus, scheduling, membership, storage, resources, and transport as separate packages so the project can grow without turning into a single centralized controller.

## Repository Layout

```text
cmd/cmesh/              CLI entrypoint
internal/agent/         Worker runtime orchestration
internal/cluster/       Shared cluster domain types
internal/config/        Configuration loading and defaults
internal/consensus/     Consensus abstraction and single-node implementation
internal/jobs/          Job model and state transitions
internal/membership/    Node registration, heartbeats, liveness
internal/resources/     Hardware/resource discovery and benchmark types
internal/scheduler/     Placement logic for jobs
internal/storage/       AI artifact cache and future object storage layer
internal/transport/     API and RPC transport boundaries
internal/version/       Build/version metadata
web/                    Dashboard application
docs/                   Architecture and project documentation
deployments/            Docker, Compose, and future deployment assets
scripts/                Development scripts
examples/               Example configs and workflows
```

## Quick Start

Start a local manager:

```sh
go run ./cmd/cmesh manager start
```

Join the local machine as a worker:

```sh
go run ./cmd/cmesh worker run --name local-dev-worker --cpu 4 --memory-gb 5 --disk-gb 50
```

Run the worker and submit initial benchmarks:

```sh
go run ./cmd/cmesh worker run --name local-dev-worker --cpu 4 --memory-gb 5 --disk-gb 50 --benchmark
```

Open the dashboard:

```text
http://localhost:8080
```

Register multiple local test workers:

```sh
go run ./cmd/cmesh dev local-cluster --workers 3
```

Submit a first echo job:

```sh
go run ./cmd/cmesh job submit --type echo --input "hello cluster"
go run ./cmd/cmesh job list
```

## Alpha Deployment

For testing workers across the internet, see [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md).

Install a macOS/Linux worker from a release in one step:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  CMESH_MANAGER_URL="https://cmesh.nythral.com" \
  CMESH_JOIN_TOKEN="replace-with-join-token" \
  sh
```

For a one-shot registration without a heartbeat loop:

```sh
go run ./cmd/cmesh worker join --name local-dev-worker --cpu 4 --memory-gb 5 --disk-gb 50
```

## Development Status

CMesh is at the initial architecture and scaffolding stage.

## License

This repository is licensed under the Apache License 2.0. See [LICENSE](LICENSE).
