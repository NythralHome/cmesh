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

Distributed execution of one large model across multiple machines is production-validated only for the documented Linux sliced-model path. See [docs/LINUX_PRODUCTION.md](docs/LINUX_PRODUCTION.md) for the current support matrix, install flow, evidence, and limitations.

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
apps/worker_desktop/    Flutter donor app for worker setup and control
docs/                   Architecture and project documentation
deployments/            Docker, Compose, and future deployment assets
scripts/                Development scripts
examples/               Example configs and workflows
```

## Worker Desktop App

The Flutter worker desktop shell gives donors a graphical way to enter a manager URL, join token, resource limits, and lifecycle actions without typing installer commands.

```sh
make worker-desktop-run
make worker-desktop-test
make worker-desktop-build
```

The app starts the local CMesh worker control API automatically when it can find a `cmesh` binary. In development, use:

```sh
make worker-desktop-run
```

`make worker-desktop-build` bundles the local Go `cmesh` binary into the desktop build for the current OS. The next step is adding signed installers and a privileged helper for OS service installation/removal.

The local control API supports `X-CMesh-Control-Token` for `/v1/*` routes. The desktop app generates and passes this token automatically when it starts the bundled control process.

The control API also manages the experimental `llama.cpp` RPC backend:

- `GET /v1/runtime/llama.cpp/rpc/status`
- `POST /v1/runtime/llama.cpp/rpc/start`
- `POST /v1/runtime/llama.cpp/rpc/stop`
- `POST /v1/runtime/llama.cpp/rpc/restart`

By default the RPC backend binds to `127.0.0.1:50052`. This is intentional: upstream `llama.cpp` documents the RPC backend as fragile and insecure for open networks, so CMesh must explicitly mediate any public or cross-machine exposure.

Worker invite links use the `cmesh://join` protocol. Release builds register that protocol through the macOS app bundle and through per-user registration on Windows/Linux when the app starts.

Tagged releases publish early desktop bundles alongside CLI binaries:

- `CMesh-Worker-Apple-Silicon.dmg`
- `CMesh-Worker-Intel-Mac.dmg`
- `CMesh-Worker-windows-amd64.zip`
- `CMesh-Worker-linux-amd64.tar.gz`

Alpha deployments should use `make deploy-alpha VERSION=v...`; it refuses to deploy until all release assets are available.

For the first multi-machine alpha, use [docs/FIRST_REAL_TEST.md](docs/FIRST_REAL_TEST.md).

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

## Linux Production

The current public release candidate is Linux-only:

- one Linux manager service;
- three Linux worker services with resident stage daemons;
- pinned `llama.cpp-b9704-linux-amd64-rpc-stage` runtime;
- Qwen2.5 14B Instruct Q4_K_M split into physical GGUF stage artifacts;
- memory-aware placement across workers.

Download and verify the release package:

```sh
VERSION=v0.1.0-linux-rc.1
BASE_URL=https://github.com/NythralHome/cmesh/releases/download/$VERSION

curl -fLO "$BASE_URL/$VERSION.tar.gz"
curl -fLO "$BASE_URL/$VERSION.tar.gz.sha256"
curl -fLO "$BASE_URL/$VERSION.tar.gz.sig"
curl -fLO "$BASE_URL/$VERSION.tar.gz.public-key.pem"

shasum -a 256 -c "$VERSION.tar.gz.sha256"
openssl dgst -sha256 \
  -verify "$VERSION.tar.gz.public-key.pem" \
  -signature "$VERSION.tar.gz.sig" \
  "$VERSION.tar.gz"

tar -xzf "$VERSION.tar.gz"
cd "$VERSION"
openssl dgst -sha256 -verify release-signing-public-key.pem -signature manifest.json.sig manifest.json
openssl dgst -sha256 -verify release-signing-public-key.pem -signature checksums.txt.sig checksums.txt
shasum -a 256 -c checksums.txt
```

Expected GitHub release assets:

- `v0.1.0-linux-rc.1.tar.gz`
- `v0.1.0-linux-rc.1.tar.gz.sha256`
- `v0.1.0-linux-rc.1.tar.gz.sig`
- `v0.1.0-linux-rc.1.tar.gz.public-key.pem`

Install a manager from the verified package:

```sh
sudo CMESH_BINARY_URL="file://$PWD/cmesh-linux-amd64" \
  CMESH_NONINTERACTIVE=true \
  CMESH_ADDR=0.0.0.0:18080 \
  CMESH_PUBLIC_URL=http://MANAGER_HOST:18080 \
  CMESH_JOIN_TOKEN=replace-with-generated-secret \
  CMESH_OPERATOR_TOKEN=replace-with-generated-secret \
  ./install-manager-linux.sh install
```

Install each worker from the same verified package:

```sh
sudo CMESH_BINARY_URL="file://$PWD/cmesh-linux-amd64" \
  CMESH_MANAGER_URL=http://MANAGER_HOST:18080 \
  CMESH_JOIN_TOKEN=replace-with-manager-join-token \
  CMESH_CPU=2 \
  CMESH_MEMORY_GB=6 \
  CMESH_DISK_GB=40 \
  CMESH_STAGE_DAEMON=true \
  CMESH_STAGE_DAEMON_BACKEND=llama.cpp-resident \
  CMESH_LLAMA_CPP_RUNTIME_AUTO=true \
  CMESH_LLAMA_CPP_RUNTIME_URL="file://$PWD/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz" \
  CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM=true \
  ./install-worker.sh install
```

Start with [docs/LINUX_PRODUCTION.md](docs/LINUX_PRODUCTION.md). The detailed
operator runbooks are:

- [docs/PRODUCTION_INSTALL.md](docs/PRODUCTION_INSTALL.md)
- [docs/LINUX_SLICED_RUNBOOK.md](docs/LINUX_SLICED_RUNBOOK.md)
- [docs/LINUX_MODEL_MATRIX.md](docs/LINUX_MODEL_MATRIX.md)
- [docs/LINUX_SECURITY_HARDENING.md](docs/LINUX_SECURITY_HARDENING.md)
- [docs/LINUX_OBSERVABILITY.md](docs/LINUX_OBSERVABILITY.md)
- [docs/LINUX_BACKUP_RESTORE.md](docs/LINUX_BACKUP_RESTORE.md)

Windows, macOS, desktop worker apps, GPU acceleration, arbitrary model slicing,
and public untrusted worker marketplaces are not part of the current production
support matrix.

## Alpha Deployment

For testing workers across the internet, see [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md).

Install a macOS/Linux worker from a release in one step:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  CMESH_MANAGER_URL="https://cmesh.nythral.com" \
  CMESH_JOIN_TOKEN="replace-with-join-token" \
  CMESH_CPU=4 \
  CMESH_MEMORY_GB=8 \
  CMESH_DISK_GB=50 \
  sh
```

When installed as a background service, the donor can inspect or stop it:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sh -s -- status
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- stop
```

For a one-shot registration without a heartbeat loop:

```sh
go run ./cmd/cmesh worker join --name local-dev-worker --cpu 4 --memory-gb 5 --disk-gb 50
```

## Development Status

CMesh is at the initial architecture and scaffolding stage.

## License

This repository is licensed under the Apache License 2.0. See [LICENSE](LICENSE).
