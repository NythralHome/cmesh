# Alpha Deployment

This guide describes a trusted alpha deployment where one internet-accessible manager coordinates workers running on machines in different locations.

CMesh is not ready for an untrusted public marketplace yet. Use invite-only workers that you know.

## Topology

```text
VPS / public server
  cmesh manager

Remote machines
  cmesh worker run -> outbound HTTPS/HTTP connection to manager
```

Workers do not need inbound ports. They connect out to the manager.

## Build Binaries

```sh
make dist
```

Artifacts:

```text
dist/cmesh-darwin-arm64
dist/cmesh-darwin-amd64
dist/cmesh-linux-amd64
dist/cmesh-linux-arm64
dist/cmesh-windows-amd64.exe
```

## Guarded Alpha Deploy

For the hosted alpha, deploy through the release-asset guard:

```sh
make deploy-alpha VERSION=v0.1.0-alpha.44
```

The guard checks that the CLI binary and every worker desktop artifact referenced by the invite page are already downloadable from GitHub Releases. If any asset is still publishing, deployment stops before the manager is updated.

## Manager On A VPS

Generate an invite token:

```sh
openssl rand -hex 32
```

Run directly:

```sh
export CMESH_JOIN_TOKEN="replace-with-generated-token"
export CMESH_OPERATOR_TOKEN="replace-with-operator-token"
export CMESH_PUBLIC_URL="https://cmesh.example.com"
./cmesh-linux-amd64 manager start \
  --addr :8080 \
  --state-path /var/lib/cmesh/cmesh-state.json
```

Or install the manager as a Linux systemd service with an interactive wizard:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | sudo sh
```

Preview what the installer will do before running it with `sudo`:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | \
  CMESH_INSTALL_DRY_RUN=true \
  CMESH_NONINTERACTIVE=true \
  CMESH_DOMAIN="cmesh.example.com" \
  sh
```

The wizard asks for a domain and can install/configure Caddy for HTTPS automatically. Point DNS before enabling HTTPS:

```text
cmesh.example.com -> VPS_PUBLIC_IP
```

If you override `CMESH_ADDR`, the installer points Caddy at the same local
manager port. For example, `CMESH_ADDR=0.0.0.0:19080` becomes
`reverse_proxy 127.0.0.1:19080`.

After installing the systemd service, the installer waits for the local manager
`/health` endpoint. A service that is active but not answering HTTP is treated
as a failed install.
Use `/health` for process liveness. Use operator-protected `/v1/observability`
for readiness: it reports worker/job/CDIP/RPC/stage-daemon counters and returns
`status: "degraded"` with concrete blockers such as no online workers, stale
running jobs, quarantined RPC endpoints, or missing stage daemons.

For cloud-init or automation, pass values non-interactively:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | \
  sudo env \
    CMESH_DOMAIN="cmesh.example.com" \
    CMESH_ADMIN_EMAIL="admin@example.com" \
    CMESH_INSTALL_CADDY=true \
    sh
```

If `CMESH_JOIN_TOKEN` is omitted, the installer generates one and stores it in `/etc/cmesh/manager.env`.
The installer does not print raw secrets by default; use
`CMESH_PRINT_SECRETS=true` only for a controlled one-off install session where
terminal logs are not retained.
The generated systemd unit uses a basic sandbox profile:
`NoNewPrivileges=true`, `PrivateTmp=true`, `ProtectSystem=strict`,
`ProtectHome=true`, and write access limited to `/var/lib/cmesh`.

CMesh uses local file persistence by default. Manager restarts do not erase workers, jobs, and benchmark history:

```text
/var/lib/cmesh/cmesh-state.json
```

For larger deployments, Postgres is optional:

```sh
export CMESH_JOIN_TOKEN="replace-with-generated-token"
export CMESH_OPERATOR_TOKEN="replace-with-operator-token"
export CMESH_PUBLIC_URL="https://cmesh.example.com"
export DATABASE_URL="postgres://user:password@host:5432/cmesh_alpha?sslmode=require"
./cmesh-linux-amd64 manager start --addr :8080
```

CMesh runs the required schema migrations on startup when Postgres is used. `CMESH_OPERATOR_TOKEN` protects the cluster dashboard, read/admin APIs, and `/invite`, where the dashboard generates worker install commands.

Production guardrail: `cmesh manager start` refuses to bind a public manager
without both a join token and an operator token. A manager is treated as public
when `--public-url` is set or `--addr` binds outside loopback, for example
`:8080`, `0.0.0.0:8080`, or a LAN/public IP. For isolated development only,
`--allow-insecure-public-manager` bypasses this check.

Workers receive a per-node auth token during join. The worker CLI stores it in
memory for the running process and sends it as `X-CMesh-Worker-Token` on
heartbeat, job polling, job completion, and leave requests. Manual `worker
poll-once` debugging against a joined node must pass `--node-auth-token` or set
`CMESH_NODE_AUTH_TOKEN`.

Run with Docker Compose:

```sh
cd deployments/docker
export CMESH_JOIN_TOKEN="replace-with-generated-token"
export CMESH_OPERATOR_TOKEN="replace-with-operator-token"
export CMESH_PUBLIC_URL="https://cmesh.example.com"
docker compose up -d --build
```

Open firewall ports `80` and `443` when using Caddy. If you skip Caddy, expose `8080` or place CMesh behind your own reverse proxy.

## Manual Domain And HTTPS With Caddy

Point DNS:

```text
cmesh.example.com -> VPS_PUBLIC_IP
```

Caddyfile:

```text
cmesh.example.com {
  reverse_proxy 127.0.0.1:8080
}
```

Workers can then connect to:

```text
https://cmesh.example.com
```

## Connect A Remote Worker

For a guided first alpha with several real machines, use [FIRST_REAL_TEST.md](FIRST_REAL_TEST.md). Prefer the desktop worker app for donors; the install scripts below are the fallback path for terminal-only machines.

macOS/Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  CMESH_MANAGER_URL="https://cmesh.example.com" \
  CMESH_JOIN_TOKEN="replace-with-generated-token" \
  CMESH_CPU=4 \
  CMESH_MEMORY_GB=8 \
  CMESH_DISK_GB=50 \
  sh
```

Linux service with boot startup:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  sudo env CMESH_MANAGER_URL="https://cmesh.example.com" \
  CMESH_JOIN_TOKEN="replace-with-generated-token" \
  CMESH_INSTALL_SERVICE=true \
  CMESH_CPU=4 \
  CMESH_MEMORY_GB=8 \
  CMESH_DISK_GB=50 \
  sh
```

On Linux service installs with the pinned CMesh llama.cpp stage runtime, the
worker installer also creates `cmesh-stage-daemon.service` on
`127.0.0.1:19781` and starts `cmesh-worker.service` with
`--stage-daemon-url http://127.0.0.1:19781`. The manager reads that endpoint
from worker runtime resources and injects it into distributed stage jobs, so
distributed decode-loop sessions do not require a manual daemon URL in the API
request.
Both worker units use the same systemd hardening profile as the manager while
keeping `/var/lib/cmesh` writable for model, runtime, cache, and session data.

Worker service control:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sh -s -- status
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- stop
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- start
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- restart
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- uninstall
```

Manual macOS/Linux run:

```sh
chmod +x ./cmesh
./cmesh worker run \
  --manager https://cmesh.example.com \
  --token replace-with-generated-token \
  --name friend-mac \
  --cpu 4 \
  --memory-gb 8 \
  --disk-gb 50 \
  --benchmark
```

Windows PowerShell:

```powershell
$env:CMESH_MANAGER_URL="https://cmesh.example.com"
$env:CMESH_JOIN_TOKEN="replace-with-generated-token"
$env:CMESH_CPU="4"
$env:CMESH_MEMORY_GB="8"
$env:CMESH_DISK_GB="50"
iwr https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.ps1 -UseB | iex
```

Windows service control:

```powershell
$script = (iwr https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.ps1 -UseB).Content
iex "& { $script } -Action status"
iex "& { $script } -Action stop"
iex "& { $script } -Action start"
iex "& { $script } -Action uninstall"
```

Manual Windows PowerShell run:

```powershell
.\cmesh.exe worker run `
  --manager https://cmesh.example.com `
  --token replace-with-generated-token `
  --name friend-pc `
  --cpu 4 `
  --memory-gb 8 `
  --disk-gb 50 `
  --benchmark
```

## Submit A Test Job

```sh
./cmesh job submit \
  --manager https://cmesh.example.com \
  --type echo \
  --input "hello from the internet"
```

Inspect jobs:

```sh
./cmesh job list --manager https://cmesh.example.com
```

## Real-Machine Distributed RPC Proof

For a repeatable temporary AWS proof with one coordinator and two remote
llama.cpp RPC backends:

```sh
CMESH_AWS_INSTANCE_COUNT=3 scripts/aws-distributed-rpc-e2e.sh
```

The script:

- Builds a Linux CMesh binary from the current checkout.
- Creates exactly three tagged EC2 instances.
- Builds or uploads the pinned llama.cpp RPC runtime.
- Starts one manager/coordinator and two RPC backend workers.
- Installs the default small GGUF model on the coordinator.
- Checks distributed RPC readiness and plan executability.
- Runs `model.generate.distributed_rpc`.
- Writes evidence under `/tmp/cmesh-distributed-e2e-<timestamp>`.
- Terminates the EC2 instances, keypair, and security group on exit.

The proof is successful only when `distributed-result.json` reports
`rpc_endpoint_count >= 2` and the generated job result includes coordinator,
backend, endpoint, runtime, model path, model bytes, and timing evidence.

Cleanup is part of the acceptance criteria. After the run,
`cleanup-instances.json` must show every instance in `terminated` state. Use
`CMESH_KEEP_AWS_RESOURCES=true` only for debugging a failed run, then terminate
the tagged resources manually.

## Real-Machine Installer Proof

Before asking real users to install CMesh on their own VPS or Linux machines,
run the installer E2E against clean temporary Ubuntu hosts:

```sh
CMESH_AWS_INSTANCE_COUNT=3 scripts/aws-installers-e2e.sh
```

The script creates one manager host and two worker hosts, builds the current
Linux binary, copies only the release-style install scripts plus that binary to
each host, then installs:

- `cmesh.service` through `scripts/install-manager-linux.sh`;
- two `cmesh-worker.service` units through `scripts/install-worker.sh`;
- manager and worker configuration through non-interactive environment values.

The proof is accepted only when:

- the manager `/health` endpoint responds;
- `/v1/cluster` reports at least two online workers using the operator token;
- all three systemd services are `active`;
- `cleanup-instances.json` shows every EC2 instance as `terminated`.

This validates the real-user Linux install path. It intentionally does not
install models or run distributed inference; use the CDIP and distributed RPC
proofs below for model execution.

## Real-Machine CDIP Stage Proof

For the CMesh-native CDIP layer-stage path, run:

```sh
CMESH_AWS_INSTANCE_COUNT=3 scripts/aws-cdip-real-gguf-e2e.sh
```

This creates one temporary manager host and two temporary stage worker hosts.
The stage workers download a real GGUF model, build the patched
`cmesh-stage-runner`, register model/layer inventory, execute prepare and
decode stage commands through worker polling, relay activation through the
manager, and complete the parent distributed job from terminal-stage output.

For the real-user install path, prefer a prebuilt CMesh stage runtime artifact
instead of compiling llama.cpp on every worker host:

```sh
scripts/prepare-current-stage-runtime-artifact.sh
CMESH_STAGE_RUNTIME_ARCHIVE=dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz \
  CMESH_AWS_INSTANCE_COUNT=3 scripts/aws-cdip-real-gguf-e2e.sh
```

For production-readiness runs, require a prebuilt artifact explicitly:

```sh
CMESH_REQUIRE_STAGE_RUNTIME_ARTIFACT=true \
CMESH_STAGE_RUNTIME_ARCHIVE=dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz \
  CMESH_AWS_INSTANCE_COUNT=3 scripts/aws-cdip-real-gguf-e2e.sh
```

With `CMESH_REQUIRE_STAGE_RUNTIME_ARTIFACT=true`, the script refuses the
developer-only fallback that compiles llama.cpp remotely on EC2.

When verifying a non-native stage runtime archive from macOS, enable static
resident-protocol enforcement before a production-like proof:

```sh
CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC=true \
scripts/verify-llamacpp-runtime-artifact.sh \
  dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz
```

This catches an older `cmesh-stage-runner` package before AWS or release
testing, even though the Linux binary cannot be executed locally on macOS.
If this strict check fails on macOS, rebuild the Linux stage runtime locally
through Docker:

```sh
JOBS=4 scripts/build-llamacpp-runtime-linux-docker.sh
```

That script produces the current Linux `rpc-stage` archive and reruns strict
resident-protocol verification before the archive is used by service installs
or AWS proofs.
`scripts/production-readiness-gate.sh` enables this resident-protocol static
requirement by default, so stale Linux stage runtime archives fail before any
EC2 instances are created.

Published release assets can be tested the same way with
`CMESH_STAGE_RUNTIME_URL`.

To include the real Linux manager installer in this proof, add
`CMESH_INSTALL_MANAGER_SERVICE=true`. This installs the manager through
`scripts/install-manager-linux.sh`, starts `cmesh.service`, verifies the service
is active, then runs the real GGUF stage proof through that installed manager:

```sh
CMESH_INSTALL_MANAGER_SERVICE=true \
CMESH_STAGE_RUNTIME_ARCHIVE=dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz \
  CMESH_AWS_INSTANCE_COUNT=3 scripts/aws-cdip-real-gguf-e2e.sh
```

For the closest current real-user path, also install both stage workers through
`scripts/install-worker.sh` as `cmesh-worker.service` units:

```sh
CMESH_INSTALL_MANAGER_SERVICE=true \
CMESH_INSTALL_STAGE_WORKER_SERVICES=true \
CMESH_STAGE_RUNTIME_ARCHIVE=dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz \
  CMESH_AWS_INSTANCE_COUNT=3 scripts/aws-cdip-real-gguf-e2e.sh
```

Before creating EC2 instances, the same script can run an artifact-only
preflight. This verifies the local runtime archive, records its SHA256, writes
`config.json`, and exits without touching AWS:

```sh
CMESH_PREFLIGHT_ONLY=true \
CMESH_STAGE_RUNTIME_ARCHIVE=dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz \
  CMESH_AWS_INSTANCE_COUNT=3 scripts/aws-cdip-real-gguf-e2e.sh
```

In this mode the stage workers join through the normal worker installer, report
model/layer inventory through heartbeats, poll jobs as long-running services,
and execute the distributed CDIP stage jobs without `worker poll-once`. The
worker installer receives the stage runtime URL/name/version plus
`CMESH_MODEL_URL`, downloads the runtime archive to
`/var/lib/cmesh/cache/runtimes/llama.cpp/<runtime-version>`, derives
`cmesh-stage-runner` from that runtime, and downloads the GGUF artifact to
`/var/lib/cmesh/models/<file>` before starting `cmesh-stage-daemon.service` and
`cmesh-worker.service`. The manager must see each worker advertise a ready
`cdip.stage-session-v1` endpoint. The same run also verifies `decode-loop`
worker-dispatch mode: a second distributed parent job schedules one repeated
decode step with a shared `kv_cache_key`, the first terminal wave reports
`final:false`, the manager schedules step 3 without carrying the test-only
terminal override forward, and the worker services complete the parent from the
follow-up terminal stage.

The run is accepted when `summary.json` reports:

- parent `status` is `succeeded`;
- result `kind` is `cdip.distributed_terminal_result`;
- `dispatch_status` is `succeeded`;
- `dispatch_result.kind` is `cdip.distributed_terminal_result`;
- `dispatch_result.step` is `3`;
- `dispatch-decode-loop.json` reports `trace.mode=worker-dispatch` and stage
  inputs contain `step=2` plus a shared `kv_cache_key`;
- `dispatch-after-partial-jobs.json` reports stage inputs with `step=3` and the
  same `kv_cache_key`;
- `stage-runtime-archive-verify.txt` reports `PASS` when
  `CMESH_STAGE_RUNTIME_ARCHIVE` is used;
- `stage-runtime-archive-sha256.txt` records the exact tested runtime artifact;
- both prepare stage jobs reached `cdip_state=ready`;
- source and terminal stage jobs reached `cdip_state=decode`;
- when `CMESH_INSTALL_MANAGER_SERVICE=true`, `manager-service.txt` is `active`;
- when `CMESH_INSTALL_STAGE_WORKER_SERVICES=true`, both
  `real-cdip-stage-*-service.txt` and
  `real-cdip-stage-*-stage-daemon-service.txt` files are `active`,
  `real-cdip-stage-*-stage-daemon-health.json` reports
  `protocol=cdip.stage-session-v1`, and `stage-nodes.json` reports
  `"source":"worker-services"`;
- when `CMESH_INSTALL_STAGE_WORKER_SERVICES=true`,
  `single-decode-*-daemon-session.json` reports `decode_steps=1` and
  `dispatch-loop-*-daemon-session.json` reports `decode_steps=2`; source and
  terminal session records must also report `last_stage_command` as
  `source_decode` / `terminal_decode` and `last_payload_bytes > 0`, proving that
  worker services used the advertised local daemon sessions for repeated decode
  steps and moved activation payloads through them;
- `cleanup-instances.json` shows every EC2 instance as `terminated`.

Current guardrail: this proves real remote stage execution and terminal decode
over the CMesh activation relay. It is not yet full production-grade multi-token
KV-cache ownership across stages.

The stage daemon has an explicit backend boundary. Production installs currently
run the default `mock` backend for lifecycle and dispatch proofs. The guarded
target backend is `llama.cpp-resident`; it advertises native KV intent through
`/health`, but refuses sessions until native llama.cpp in-process stage loading,
per-stage KV ownership, and resident decode hooks are implemented.
Use `--runner-bin` or `CMESH_STAGE_RUNNER_BIN` with `llama.cpp-resident` so
health checks can verify the pinned stage runner binary separately from the
missing native hooks.
Session creation with `llama.cpp-resident` also requires `model_path`; the
daemon runs the pinned stage runner's `prepare` command against the requested
layer range before accepting a not-yet-decode-ready resident session.
The daemon now has a formal resident runner boundary:
`cmesh-stage-runner --command resident-capabilities` must return
`kind=cmesh.llamacpp_resident_capabilities` with
`protocol=cdip.llamacpp-resident-runner-v1`, `native_kv=true`,
`persistent_model=true`, `persistent_kv_in_memory=true`, and `decode_hook=true`
before the backend can mark sessions `resident_ready`. Decode then calls
`cmesh-stage-runner --command resident-decode` for the same session. Until the
patched llama.cpp runner implements that protocol, the backend remains blocked
and does not claim production sliced execution.
For Linux service installs, `scripts/install-worker.sh` writes
`CMESH_STAGE_DAEMON_BACKEND` into `/etc/cmesh/worker.env` and starts
`cmesh-stage-daemon.service` with `--backend` plus `--runner-bin`. The default
backend is `mock`; use `llama.cpp-resident` only for guarded native backend
tests until decode/KV hooks are complete.
The Linux worker installer creates a dedicated `cmesh` system user, owns
`/var/lib/cmesh` with that account, and runs both `cmesh-worker.service` and
`cmesh-stage-daemon.service` as `User=cmesh` / `Group=cmesh`. Service writes are
kept under `/var/lib/cmesh`; `/etc/cmesh/worker.env` remains root-owned with
mode `600` because it contains the join token and runtime configuration.

The daemon persists session metadata atomically under its `--session-dir` so
operators can inspect session lifecycle, decode counters, backend kind, and the
last stage command, payload byte count, tensor sequence/checksum after failures.
This is intentionally metadata recovery only; a restarted daemon must not
pretend that previous native in-memory KV state is still resident.
The production recovery rules for stale jobs, missing daemon sessions, cancel
cascades, and resident session cleanup are defined in
[`docs/protocol/cdip-reliability-recovery-v1.md`](protocol/cdip-reliability-recovery-v1.md).

`scripts/production-readiness-gate.sh` includes the installer dry-run smoke and
observability smoke, so changes to Linux manager/worker service wiring, runtime
artifact selection, stage daemon backend flags, or operator readiness reporting
are checked before local CDIP and AWS preflight steps.
The same gate also runs the local resident prepare-probe smoke, which exercises
the real stage daemon process in guarded `llama.cpp-resident` mode without
claiming decode/KV readiness.

Dashboard:

```text
https://cmesh.example.com
```

Invite page:

```text
https://cmesh.example.com/invite
```

The dashboard prompts for `CMESH_OPERATOR_TOKEN` before showing cluster state or worker install commands.

## Current Alpha Limits

- Manager state is durable in the local state file by default. Postgres is optional for larger deployments.
- Join token protects worker registration. Operator token protects the dashboard and read/admin API for alpha deployments.
- Transport security depends on your reverse proxy. Use HTTPS for internet tests.
- Workers execute only supported CMesh job types. Current executor: `echo`.
- This is a trusted private alpha, not a public compute marketplace.
