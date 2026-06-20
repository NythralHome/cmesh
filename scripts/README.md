# Scripts

## Worker Install

macOS/Linux one-shot worker runner:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  CMESH_MANAGER_URL="https://cmesh.nythral.com" \
  CMESH_JOIN_TOKEN="replace-with-join-token" \
  CMESH_CPU=4 \
  CMESH_MEMORY_GB=8 \
  CMESH_DISK_GB=50 \
  sh
```

Linux worker as a systemd service:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  sudo env CMESH_MANAGER_URL="https://cmesh.nythral.com" \
  CMESH_JOIN_TOKEN="replace-with-join-token" \
  CMESH_INSTALL_SERVICE=true \
  CMESH_CPU=4 \
  CMESH_MEMORY_GB=8 \
  CMESH_DISK_GB=50 \
  sh
```

For systemd installs, the worker installer waits up to 30 seconds for
`cmesh-worker.service` to become active and fails with `systemctl status` if it
does not. On Linux service installs with the pinned llama.cpp stage runtime, the
installer also creates `cmesh-stage-daemon.service` bound to
`127.0.0.1:19781`, stores sessions under `/var/lib/cmesh/stage-sessions`, and
starts the worker with `--stage-daemon-url http://127.0.0.1:19781` so the
manager can automatically schedule resident stage sessions.

The installer uses the latest GitHub release by default. Pin a specific release
with `CMESH_VERSION=v0.1.0-alpha.N` when you need reproducible fleet rollout.
For staging or pre-release tests, `CMESH_BINARY_URL` can point directly at a
specific `cmesh-linux-*` or `cmesh-darwin-*` binary.

Linux service workers can also pre-download a model artifact during install and
advertise it immediately in worker heartbeats:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  sudo env CMESH_MANAGER_URL="https://cmesh.example.com" \
  CMESH_JOIN_TOKEN="replace-with-join-token" \
  CMESH_INSTALL_SERVICE=true \
  CMESH_STAGE_RUNNER_BIN="/var/lib/cmesh/stage-runtime/bin/cmesh-stage-runner" \
  CMESH_MODEL_ID="qwen2.5-0.5b-instruct-q4-k-m" \
  CMESH_MODEL_URL="https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/qwen2.5-0.5b-instruct-q4_k_m.gguf" \
  CMESH_MODEL_LAYERS=24 \
  sh
```

When `CMESH_MODEL_URL` is set and `CMESH_MODEL_PATH` is omitted, Linux service
installs download the model to `/var/lib/cmesh/models/<file>`. Use
`CMESH_MODEL_FILE` to override the filename and `CMESH_MODEL_PATH` to point at
an already-present artifact.

For CDIP stage-worker hosts, the installer can also pre-download and extract a
pinned CMesh llama.cpp stage runtime. When
`CMESH_LLAMA_CPP_RUNTIME_URL` and `CMESH_LLAMA_CPP_RUNTIME_VERSION` are set and
`CMESH_STAGE_RUNNER_BIN` is omitted, the installer derives the stage runner path
as:

```text
/var/lib/cmesh/cache/runtimes/llama.cpp/<runtime-version>/bin/cmesh-stage-runner
```

This is the preferred service install path because the worker service starts
with both the runtime and the model artifact already present, and the local
stage daemon is available for CDIP resident stage-session tests.
By default the installer starts the stage daemon with `CMESH_STAGE_DAEMON_BACKEND=mock`.
Set `CMESH_STAGE_DAEMON_BACKEND=llama.cpp-resident` only when testing the
guarded native resident backend path; the systemd unit passes both
`--backend` and `--runner-bin` to `cmesh stage-runner daemon`.
Installer dry-runs print `stage_daemon_service_command` so CI can verify the
exact systemd daemon command before any host is modified.

Preview the install plan without downloading binaries or starting the worker:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  CMESH_INSTALL_DRY_RUN=true \
  CMESH_NONINTERACTIVE=true \
  CMESH_MANAGER_URL="https://cmesh.example.com" \
  CMESH_JOIN_TOKEN="replace-with-join-token" \
  sh
```

Linux worker as a distributed RPC backend:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  sudo env CMESH_MANAGER_URL="https://cmesh.example.com" \
  CMESH_JOIN_TOKEN="replace-with-join-token" \
  CMESH_INSTALL_SERVICE=true \
  CMESH_CPU=4 \
  CMESH_MEMORY_GB=8 \
  CMESH_DISK_GB=50 \
  CMESH_RPC=true \
  CMESH_RPC_HOST="0.0.0.0" \
  CMESH_RPC_PORT=50052 \
  sh
```

If the manager and workers run in a private VPC or VPN, leave
`CMESH_RPC_ADVERTISE_HOST` blank and the worker will infer the source IP used to
reach the manager. For public internet workers, put RPC behind a VPN or private
network; llama.cpp RPC is not an authenticated public protocol.

Worker service control:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sh -s -- status
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- stop
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- start
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- restart
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- uninstall
```

Windows PowerShell:

```powershell
$env:CMESH_MANAGER_URL="https://cmesh.nythral.com"
$env:CMESH_JOIN_TOKEN="replace-with-join-token"
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

## Pinned llama.cpp RPC Runtime

CMesh can use a pinned llama.cpp runtime archive instead of whatever `llama-cli`
happens to be installed on the host. This is the recommended path for
distributed RPC tests because every coordinator and RPC backend should run the
same llama.cpp build.

Build a local runtime archive:

```sh
LLAMA_CPP_REF=b9704 JOBS=4 scripts/build-llamacpp-runtime.sh
```

Build the CMesh CDIP stage runtime archive when testing layer-stage execution:

```sh
LLAMA_CPP_REF=b9704 JOBS=4 CMESH_LLAMA_CPP_STAGE_RUNNER=true scripts/build-llamacpp-runtime.sh
```

For production-style sliced-model tests, first prepare the pinned Linux amd64
stage artifact that AWS workers will install instead of compiling llama.cpp on
the instance:

```sh
scripts/prepare-current-stage-runtime-artifact.sh
```

The helper writes and verifies:

```text
dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz
```

On non-Linux hosts, provide either `CMESH_STAGE_RUNTIME_ARCHIVE` or
`CMESH_STAGE_RUNTIME_URL`; otherwise the helper fails with an explicit message.

Publish the generated `dist/runtimes/*.tar.gz` somewhere reachable by workers,
then pass these variables to the worker process or Linux service installer:

```sh
CMESH_LLAMA_CPP_RUNTIME_URL="https://example.com/llama.cpp-b9704-linux-amd64-rpc.tar.gz"
CMESH_LLAMA_CPP_RUNTIME_NAME="llama.cpp-b9704-linux-amd64-rpc.tar.gz"
CMESH_LLAMA_CPP_RUNTIME_VERSION="llama.cpp-b9704-linux-amd64-rpc"
CMESH_LLAMA_CPP_PREFER_CACHE=true
```

`CMESH_LLAMA_CPP_PREFER_CACHE=true` prevents a random system `llama-cli` from
winning over the pinned CMesh runtime.

On Linux amd64, `scripts/install-worker.sh` defaults to
`CMESH_LLAMA_CPP_RUNTIME_AUTO=true` and points the worker at the release
`llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz` asset for the selected
`CMESH_VERSION`; when `CMESH_VERSION=latest`, it uses GitHub's
`releases/latest/download` URL. Set `CMESH_LLAMA_CPP_RUNTIME_AUTO=false` or
provide `CMESH_LLAMA_CPP_RUNTIME_URL` to override that behavior.

## Distributed RPC Smoke

After a manager, one coordinator worker with the model installed, and at least
two RPC backend workers are
online, run:

```sh
CMESH_MANAGER_URL="https://alpha.cmesh.nythral.com" \
CMESH_OPERATOR_TOKEN="replace-with-operator-token" \
scripts/distributed-rpc-smoke.sh
```

The smoke script refreshes `/v1/runtime/rpc-pool`, installs the default small
model on the selected coordinator if needed, submits
`model.generate.distributed_rpc`, waits for completion, and prints the latest
`/v1/distributed-runs` records.

## Distributed RPC Benchmark

After the smoke path works, run a local-vs-distributed benchmark:

```sh
CMESH_MANAGER_URL="https://alpha.cmesh.nythral.com" \
CMESH_OPERATOR_TOKEN="replace-with-operator-token" \
scripts/distributed-rpc-benchmark.sh
```

The benchmark refreshes the RPC pool, runs one local coordinator-only generate
job, then runs controlled `model.generate.distributed_rpc` jobs with one, two,
and three backend endpoints when available. It writes job evidence and
`summary.json` to `/tmp/cmesh-distributed-rpc-benchmark-<timestamp>`.

## CDIP Stage Runner Spike

For layer-sharding development, validate a stage job input locally:

```sh
cmesh stage-runner prepare --input stage.json --mode logical
cmesh stage-runner decode --parent-job job-parent --stage-job job-stage-0 --stage-index 0 --payload activation --downstream-node node-b --manager http://127.0.0.1:18080
cmesh stage-runner probe-llamacpp --llama-cli /path/to/llama-cli
```

The logical runner validates the CDIP stage contract. The llama.cpp stage probe
now reports the implemented selected-tensor and Qwen2 hidden-input patch hooks,
and the CLI `source-decode`, `relay-decode`, and `terminal-decode` bridges can move prompt-to-hidden,
relayed activation, and terminal hidden-to-token frames through a local patched stage runner. A worker can execute that bridge from a
`model.generate.distributed.stage` job when the input includes
`stage_runner_bin` and `model_path`. Worker `prepare` now invokes
`cmesh-stage-runner prepare` for real GGUF stage metadata when those paths are
provided, then `stage_command: "source_decode"`, `stage_command:
"relay_decode"`, and `stage_command: "terminal_decode"` move activation frames
through the manager relay. The CDIP decode endpoint can dispatch a minimum
two-stage source-to-terminal graph with `{"mode":"relay_decode"}`. Source
activation falls back to deterministic mock data only when no runner/model path
is available. Parent jobs can now complete from terminal token output,
including a multi-token `tokens`/`output` contract. The real GGUF path now has
token feedback and file-backed per-stage KV continuity; automatic runner
discovery and long-lived daemon-owned model/KV sessions are still pending.

`cmesh stage-runner daemon` is the first production-shaped stage session
process. It exposes `/health`, `POST /v1/sessions`, `GET /v1/sessions/{id}`,
`POST /v1/sessions/{id}/decode`, and `DELETE /v1/sessions/{id}`. The current
daemon keeps session lifecycle and decode-step counters in memory and returns a
`cdip.stage-session-v1` daemon session contract. Workers advertise this endpoint
through their runtime snapshot, and the manager automatically carries it into
the per-stage job input. Distributed stage jobs can also opt into this path with
an explicit `stage_daemon_url`: prepare creates the daemon session and decode
jobs post the structured tensor envelope to the same session id. The next runtime
step is to replace the daemon decode scaffold with a native llama.cpp context
that keeps the selected stage and KV cache resident in memory.

The daemon backend is explicit. The default is `mock`, which preserves the
HTTP/session contract for repeatable tests. `--backend llama.cpp-resident`
exists as the guarded production target and currently returns a clear missing
native hooks error on session creation until the in-process llama.cpp stage load,
per-stage KV ownership, and decode hooks are implemented:

```sh
cmesh stage-runner daemon --backend mock --addr 127.0.0.1:19781
cmesh stage-runner daemon --backend llama.cpp-resident --runner-bin /path/to/cmesh-stage-runner --addr 127.0.0.1:19781
```

Session metadata is written atomically to `--session-dir` as
`<session-id>.json` on create and decode, then removed on session delete. This
is an audit/recovery trail for daemon state; it does not claim that in-memory
model/KV state survives a daemon restart.

`/health` includes `backend_status` with `runner_ready`, `missing_hooks`, and a
blocker message so production checks can distinguish "runner missing" from
"native llama.cpp hooks not implemented yet".

When `llama.cpp-resident` receives a session create request with `model_path`,
it runs a pinned `cmesh-stage-runner --command prepare` probe for the requested
stage range. A successful probe creates a not-yet-decode-ready session with
`runtime_status=prepare_probe_ready_missing_native_decode_hooks`; decode remains
blocked until native in-process stage/KV hooks are implemented.
The production runner hook is explicit: the same binary may advertise
`cdip.llamacpp-resident-runner-v1` through
`--command resident-capabilities`. Only when that report says native KV,
persistent model, persistent KV, and decode hooks are ready will the daemon
mark sessions `resident_ready`; decode is then delegated to
`--command resident-decode` for that session. This gives the upcoming patched
llama.cpp resident runner a stable contract without letting the current scaffold
pretend to be real sliced inference.

`scripts/cdip-resident-prepare-probe-smoke.sh` starts the real daemon process in
`llama.cpp-resident` mode with a deterministic fake pinned runner, creates a
session with `model_path`, verifies `/health.backend_status`, and checks the
persisted session metadata. This is the local guarded-resident proof before
native llama.cpp decode hooks are wired.

`scripts/cdip-resident-runner-contract-smoke.sh` exercises the positive side of
that same backend boundary. It uses a deterministic fake runner that implements
`resident-capabilities` and `resident-decode`, then verifies that the real HTTP
daemon reports `resident_ready`, preserves native-KV session metadata, delegates
decode through the runner contract, validates a base64 activation payload
against the tensor envelope checksum, and persists decode counters. This is
still a contract smoke, not a claim that llama.cpp itself already has native
resident stage hooks.

`scripts/llamacpp-resident-loop-smoke.sh` builds the CMesh-owned
`cmesh-stage-runner`, starts `--command resident-loop` as a real long-lived
process, and verifies capabilities, prepare, decode, complete, and shutdown
JSON-line exchanges. This is the production runner transport scaffold for the
next step: moving model/context construction and per-stage KV ownership into
that resident process.

`scripts/llamacpp-resident-native-prepare-smoke.sh` is the first resident
native-prepare proof. It uses a real GGUF fixture, starts `--command
resident-loop`, sends `native_prepare=1`, and verifies that the long-lived
process has loaded selected stage tensors and created a persistent
`llama_context`. Decode is still expected to return
`blocked_missing_native_decode_hooks`, which keeps the guardrail that M7 is not
complete until resident `llama_decode` and KV token-loop ownership are wired.

`scripts/llamacpp-resident-source-decode-smoke.sh` is the first native
resident `llama_decode` proof. It native-prepares stage 0, sends
`stage_command=source_decode token_id=1`, verifies that the already-resident
`llama_context` writes an f32 hidden-state tensor, then sends a second token and
checks that the in-memory token position advances without recreating the
session.

`scripts/cdip-resident-native-prepare-daemon-smoke.sh` connects that native
prepare path through the actual `cmesh stage-runner daemon --backend
llama.cpp-resident` HTTP API. It builds the CMesh-owned stage runner, starts a
daemon, creates a real GGUF stage session, and verifies that the session is
persisted with `persistent_model=true`, `persistent_kv_in_memory=true`, and
`ready=false`. This proves daemon integration without claiming resident decode
readiness.

`scripts/cdip-daemon-decode-loop-smoke.sh` is the local production-shaped
session proof. It starts a temporary manager and stage daemon, registers two
logical workers that advertise the daemon endpoint, prepares two daemon
sessions, dispatches a three-token decode loop through real worker polling, and
verifies that each stage session reaches `decode_steps: 3` without being
recreated. The distributed-generate request intentionally omits
`stage_daemon_url`; the manager must discover it from worker runtime resources.
This is still a daemon lifecycle proof with a fake stage runner, not native
llama.cpp resident KV ownership.

`scripts/cdip-recovery-cleanup-smoke.sh` verifies the M11 recovery cleanup
contract. It starts a temporary manager and stage daemon, creates a resident
session, creates a distributed parent plus stage job, cancels the parent, and
asserts that the stage job is aborted and the daemon session is removed.

`scripts/cdip-daemon-session-recreate-smoke.sh` verifies missing-session
recovery through the real manager/worker/daemon path. It prepares stage daemon
sessions, deletes them manually to simulate daemon session loss, dispatches one
decode wave through `worker poll-once`, and asserts that the worker recreates
the deterministic sessions before completing the distributed parent.

`scripts/production-security-smoke.sh` verifies the production security
guardrails. It confirms that a public manager refuses to start without join and
operator tokens, starts a protected manager with tokens, rejects missing and
wrong join/operator tokens, joins two workers, checks that per-node worker auth
tokens are not exposed through `/v1/nodes`, and asserts that heartbeat, job
polling, and leave requests require the correct per-node
`X-CMesh-Worker-Token`. It also starts the local worker control API and verifies
that control routes reject missing or wrong `X-CMesh-Control-Token` values.

`scripts/cdip-real-runner-dispatch-smoke.sh` exercises manager-to-worker
contract wiring for the real stage runner path. It starts a temporary manager,
registers synthetic workers, creates a distributed job with `stage_runner_bin`
and `model_path`, advances to relay decode, and verifies that scheduled worker
stage jobs contain source/relay/terminal commands plus stage-specific work dirs.

`scripts/cdip-worker-execution-smoke.sh` goes one step further: it uses the
actual worker `poll-once` execution path to run prepare, source decode, and
terminal decode jobs against a deterministic fake `cmesh-stage-runner`. This
proves manager dispatch, worker job polling, HTTP activation relay, and terminal
parent completion without requiring a real GGUF file. It also verifies the
decode-loop worker-dispatch mode by scheduling one repeated decode step with a
shared `kv_cache_key`, polling the source and terminal workers, and completing
the parent from the terminal stage output. The fake terminal runner first
returns `final:false`, which proves that the manager schedules the next loop
step, carries the previous terminal `next_token_id` back into the next source
stage as `previous_token_id`, and that workers can poll and complete that
follow-up step.

`scripts/cdip-real-gguf-worker-execution-smoke.sh` replaces the fake runner
with the patched llama.cpp `cmesh-stage-runner` and a real GGUF model. It first
reads `n_layer` from `cmesh-stage-runner prepare`, reports that layer count in
worker model inventory, then uses worker `poll-once` to execute prepare, source
decode, and terminal decode jobs through the manager relay. It also verifies a
`decode-loop` worker-dispatch step against the same real GGUF runner path.
The smoke forces the first terminal decode wave to report `final:false`, proves
that the manager schedules the next wave with the same `kv_cache_key`, then
polls the source and terminal workers again and completes the parent from the
second terminal result. The repeated source wave receives the previous terminal
token as `previous_token_id`, so it no longer has to replay only the original
prompt. The worker now sets `CMESH_STAGE_SESSION_FILE` from the shared
`kv_cache_key`; the patched runner saves and reloads native llama.cpp sequence
state for each stage, and the smoke asserts that the second decode wave loads a
non-empty `.seq` file and advances the sequence position. This is a real GGUF
stage-runner orchestration proof with file-backed per-stage KV continuity, but
it is still not production KV-sharded inference because each repeated step
re-enters the runner process instead of being served by a long-lived stage
daemon:

```sh
CMESH_GGUF_MODEL_PATH=/path/to/qwen.gguf \
CMESH_MODEL_ID=qwen2.5-0.5b-instruct-q4-k-m \
scripts/cdip-real-gguf-worker-execution-smoke.sh
```

For a self-contained local dev run, allow the smoke to download and cache the
small Qwen2.5 0.5B GGUF fixture under `/tmp/cmesh-gguf-fixtures`:

```sh
CMESH_DOWNLOAD_GGUF_FIXTURE=1 \
scripts/cdip-real-gguf-worker-execution-smoke.sh
```

For normal worker installs/repairs, set `CMESH_STAGE_RUNNER_BIN` to the patched
`cmesh-stage-runner` path before the worker writes model manifests. The worker
will best-effort probe `n_layer` and store it in model inventory. If the runner
is unavailable, CMesh falls back to the catalog layer estimate.

`scripts/cdip-decode-loop-smoke.sh` exercises the current control-plane decode
loop proof. It creates a temporary three-stage distributed job, runs prepare and
prefill, calls `/v1/cdip/jobs/{id}/decode-loop`, and verifies that the parent
job completes with a deterministic multi-token output contract plus session and
KV cache trace metadata. The same smoke also creates a second job and verifies
`{"mode":"dispatch","step":2}`: each worker receives a scheduled
`source_decode`, `relay_decode`, or `terminal_decode` stage job carrying the
same `kv_cache_key`. It then submits a terminal `final:false` result and checks
that the manager automatically schedules step 3 with the same KV cache key while
leaving the parent job open.

`scripts/llamacpp-stage-pipeline-e2e-smoke.sh` is the first real GGUF local
stage pipeline proof. When `CMESH_GGUF_MODEL_PATH` points to a compatible GGUF
model, it builds the patched llama.cpp runner, splits the model into three layer
ranges, runs source, relay, and terminal decode commands, and verifies that a
terminal token is produced from real stage activations.

It can also download the cached tiny Qwen fixture:

```sh
CMESH_DOWNLOAD_GGUF_FIXTURE=1 \
scripts/llamacpp-stage-pipeline-e2e-smoke.sh
```

`scripts/verify-llamacpp-runtime-artifact.sh` validates a packaged runtime
archive before it is uploaded or used by installer E2E. It checks the expected
archive name, required binaries, executable bits, and runs
`cmesh-stage-runner --probe` plus
`cmesh-stage-runner --command resident-capabilities` for native `rpc-stage`
archives. For non-native `rpc-stage` archives, it cannot execute the binary, so
it statically checks whether the packaged `cmesh-stage-runner` contains the
`cdip.llamacpp-resident-runner-v1` and
`cdip.llamacpp-resident-loop-v1` resident protocol strings. Set
`CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC=true` to make a missing resident
protocol fatal before using an archive for production-like or AWS proofs:

```sh
scripts/verify-llamacpp-runtime-artifact.sh \
  dist/runtimes/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz

CMESH_REQUIRE_RESIDENT_PROTOCOL_STATIC=true \
scripts/verify-llamacpp-runtime-artifact.sh \
  dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz
```

On macOS, rebuild the Linux stage runtime locally with Docker when the current
archive is stale:

```sh
JOBS=4 scripts/build-llamacpp-runtime-linux-docker.sh
```

The Docker builder packages `llama-cli`, `rpc-server`, and `cmesh-stage-runner`,
then runs both the normal verifier and strict resident-protocol static verifier
against `dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz`.

`scripts/aws-cdip-real-gguf-e2e.sh` runs the same archive verifier automatically
when `CMESH_STAGE_RUNTIME_ARCHIVE` is set. Use `CMESH_PREFLIGHT_ONLY=true` to
record `stage-runtime-archive-verify.txt`, `stage-runtime-archive-sha256.txt`,
and `config.json` without creating EC2 instances:

```sh
CMESH_PREFLIGHT_ONLY=true \
CMESH_STAGE_RUNTIME_ARCHIVE=dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz \
  scripts/aws-cdip-real-gguf-e2e.sh
```

To run the full local two-stage CDIP lifecycle smoke:

```sh
scripts/cdip-two-stage-smoke.sh
```

The smoke starts a temporary in-memory manager on localhost, registers two
synthetic logical-stage workers, creates a distributed stage graph, runs
prepare/prefill/decode/complete, verifies an activation frame through the HTTP
relay, then stops the temporary manager. Evidence is written to
`/tmp/cmesh-cdip-two-stage-smoke-<timestamp>`.

To run the local llama.cpp stage decode bridge smoke with a real Qwen GGUF:

```sh
CMESH_GGUF_MODEL_PATH=/path/to/qwen.gguf \
CMESH_STAGE_START=1 \
CMESH_STAGE_END=1 \
scripts/llamacpp-stage-decode-bridge-smoke.sh
```

The smoke builds the patched `cmesh-stage-runner` when needed, prepares a
middle-stage selected tensor materialization plan, writes a deterministic F32
hidden activation file, runs `llama_decode` through `llama_batch.embd`, and
checks that an output activation file is produced. This is local file-based
stage execution, not the remote CDIP decode loop yet.

## AWS Distributed RPC E2E

To run the full temporary AWS proof with one coordinator and two RPC backends:

```sh
scripts/aws-distributed-rpc-e2e.sh
```

The script creates three tagged EC2 instances, builds or uploads the pinned
llama.cpp RPC runtime, installs the default small model on the coordinator,
runs `model.generate.distributed_rpc`, writes evidence to `/tmp/<run-id>`, and
terminates the EC2 instances, keypair, and security group on exit.

Safety defaults:

- `CMESH_AWS_INSTANCE_COUNT` must be exactly `3` for this E2E.
- The run records AWS identity, config, node state, RPC plan, job result, and
  remote logs in the evidence directory.
- Cleanup records `cleanup-started.json` and `cleanup-instances.json`; every
  instance must end as `terminated`.
- Set `CMESH_KEEP_AWS_RESOURCES=true` only when debugging and clean up the
  tagged resources manually afterward.

## AWS CDIP Real GGUF Stage E2E

To run the temporary AWS proof for the CMesh CDIP layer-stage path:

```sh
scripts/aws-cdip-real-gguf-e2e.sh
```

The script creates exactly three EC2 instances:

- one manager/coordinator host;
- two stage worker hosts;
- a real Qwen2.5 0.5B GGUF model on each stage host.

It builds the patched `cmesh-stage-runner` on both stage workers, registers
logical stage workers with installed model/layer inventory, creates a
`model.generate.distributed` parent job, drives prepare/prefill/decode through
the manager relay, runs worker `poll-once` remotely for each stage, and verifies
that the parent job completes with a `cdip.distributed_terminal_result`. The
decode-loop proof also copies the final source and terminal runner reports back
from the EC2 stage hosts and asserts that file-backed native llama.cpp sequence
state was loaded, saved, and resumed from a non-zero sequence position on the
second token wave.

For a production-like install path, build or publish the stage runtime archive
once and pass it to the E2E instead of compiling llama.cpp on every worker:

```sh
scripts/prepare-current-stage-runtime-artifact.sh
CMESH_STAGE_RUNTIME_ARCHIVE=dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz \
  scripts/aws-cdip-real-gguf-e2e.sh
```

To verify the manager through the real Linux installer and systemd service in
the same run:

```sh
CMESH_INSTALL_MANAGER_SERVICE=true \
CMESH_STAGE_RUNTIME_ARCHIVE=dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz \
  scripts/aws-cdip-real-gguf-e2e.sh
```

That mode installs `cmesh.service` via `scripts/install-manager-linux.sh`, keeps
CDIP auto-advance disabled through `CMESH_EXTRA_MANAGER_ARGS`, checks
`manager-service.txt` for `active`, then runs the real GGUF stage proof through
the installed manager.

To also verify the stage workers through the real Linux worker installer and
systemd service path:

```sh
CMESH_INSTALL_MANAGER_SERVICE=true \
CMESH_INSTALL_STAGE_WORKER_SERVICES=true \
CMESH_STAGE_RUNTIME_ARCHIVE=dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz \
  scripts/aws-cdip-real-gguf-e2e.sh
```

That mode installs both stage hosts with `scripts/install-worker.sh`, passes
the stage runtime URL/name/version, `CMESH_MODEL_URL`, and model metadata into
`cmesh-worker.service`, lets the installer download both the runtime archive
and model artifact, starts `cmesh-stage-daemon.service`, waits for the manager
to see model-ready online workers with advertised `cdip.stage-session-v1`
daemon endpoints, and lets the services poll and execute the CDIP stage jobs
themselves. The E2E now runs two remote distributed jobs: the baseline terminal
decode proof and a `decode-loop` worker-dispatch proof that schedules a repeated
decode step with shared `kv_cache_key` metadata through the same worker
services. The dispatch proof forces the first terminal wave to report
`final:false`, verifies that the manager schedules step 3, clears the test-only
terminal override, completes the parent from the follow-up terminal stage, and
checks the local stage daemon session counters on both stage EC2 hosts.

or:

```sh
CMESH_STAGE_RUNTIME_URL="https://example.com/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz" \
  scripts/aws-cdip-real-gguf-e2e.sh
```

Evidence is written to `/tmp/cmesh-cdip-real-gguf-e2e-<timestamp>`, including:

- `distributed-generate.json`
- `after-prepare-*.json`
- `after-source-*.json`
- `after-terminal-*.json`
- `summary.json`
- `cleanup-instances.json`

## Production Readiness Gate

`scripts/production-readiness-gate.sh` is the one-command gate for real-user
readiness. By default it runs local checks and the runtime artifact preflight
without creating AWS resources:

```sh
scripts/production-readiness-gate.sh
```

The default gate requires a stage runtime archive at
`dist/runtimes-linux-amd64-current/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz`.
Override it with `CMESH_STAGE_RUNTIME_ARCHIVE=/path/to/archive.tar.gz`.
The gate prepares/verifies this artifact first and passes
`CMESH_REQUIRE_STAGE_RUNTIME_ARTIFACT=true` to the AWS CDIP script, so the
production path cannot silently fall back to compiling llama.cpp on EC2.
It also requires the stage runner archive to contain the resident runner
protocol by default. If the archive is stale on macOS, rebuild it locally:

```sh
JOBS=4 scripts/build-llamacpp-runtime-linux-docker.sh
scripts/production-readiness-gate.sh
```

It also runs the Linux manager/worker installer dry-run smoke, including the
stage daemon backend/runner wiring checks, and the observability smoke before
any AWS E2E is allowed. The observability smoke verifies that
`/v1/observability` is operator-protected, reports degraded/no-worker status,
then reports worker, job, CDIP, stage-daemon, and recovery counters after a
controlled worker join.

Run the full AWS gate only when temporary EC2 usage is intended:

```sh
CMESH_RUN_AWS_E2E=true scripts/production-readiness-gate.sh
```

The full gate runs the Linux installer E2E and the real GGUF CDIP service-worker
E2E, then relies on each AWS script's cleanup trap to terminate instances.

This is the first real-machine CDIP stage-worker proof. The current guardrail
still applies: it proves remote stage execution and terminal decode through the
activation relay and file-backed per-stage KV continuity, but long-lived
daemon-owned KV sessions are still pending.

## Manager Install

Linux VPS with systemd:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | sudo sh
```

The manager installer also uses the latest GitHub release by default. Pin with
`CMESH_VERSION=v0.1.0-alpha.N`, or set `CMESH_BINARY_URL` for staging and
pre-release validation.

Preview the manager install plan without downloading binaries, writing systemd
units, or touching Caddy:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | \
  CMESH_INSTALL_DRY_RUN=true \
  CMESH_NONINTERACTIVE=true \
  CMESH_DOMAIN="cmesh.example.com" \
  sh
```

Validate manager and worker installer dry-runs together on Linux amd64:

```sh
scripts/installers-dry-run-smoke.sh
```

Run the real installer path on clean temporary AWS Ubuntu hosts:

```sh
CMESH_AWS_INSTANCE_COUNT=3 scripts/aws-installers-e2e.sh
```

This creates one manager and two worker EC2 instances, installs
`cmesh.service` and two `cmesh-worker.service` units through the same installer
scripts users run, verifies the manager health endpoint, verifies
`workers_online >= 2` through the operator-protected cluster API, records
evidence under `/tmp/cmesh-installers-e2e-<timestamp>`, then terminates all
temporary AWS resources. The script is an installer/readiness proof, not a
model execution proof.

When `CMESH_ADDR` is overridden, the manager installer uses the same local port
for the generated Caddy reverse proxy target.

Non-interactive VPS install with Caddy HTTPS:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | \
  sudo env \
    CMESH_DOMAIN="cmesh.example.com" \
    CMESH_ADMIN_EMAIL="admin@example.com" \
    CMESH_INSTALL_CADDY=true \
    sh
```

If `CMESH_JOIN_TOKEN` is omitted, the manager installer generates one and stores it in `/etc/cmesh/manager.env`.

Manager service control:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | sh -s -- status
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | sudo sh -s -- restart
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | sudo sh -s -- stop
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | sudo sh -s -- start
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | sudo sh -s -- uninstall
```

## Alpha Deploy Guard

Run a local pre-release dry-run before creating or publishing a tag:

```sh
make release-dry-run VERSION=v0.1.0-alpha.local
```

This runs Go tests, Flutter worker tests, local CLI build, host desktop build,
embedded `cmesh` verification, installer dry-run smoke, production security
smoke, observability smoke, manager dashboard/API smoke checks, and writes local
artifacts under `dist/release-dry-run`. It does not publish a GitHub release,
push a tag, or deploy to the VPS.

On macOS the dry-run also packages a local DMG. To skip desktop packaging while debugging the script:

```sh
CMESH_DRY_RUN_SKIP_DESKTOP_BUILD=true scripts/release-dry-run.sh
```

Run the production readiness gate after a passing dry-run. The Make target
writes evidence to `dist/production-readiness`, which the release candidate
script requires:

```sh
make production-readiness
```

For a public RC, run the same gate with temporary AWS E2E enabled:

```sh
CMESH_RUN_AWS_E2E=true make production-readiness
```

Prepare release candidate metadata after both reports pass:

```sh
make release-candidate VERSION=v0.1.0-alpha.83
```

This writes release notes, expected asset names, local dry-run checksums, the
production readiness report, and a manifest under
`dist/release-candidate/<version>`. It refuses to run without
`dist/release-dry-run/report.txt` and `dist/production-readiness/report.txt`
unless explicitly overridden for local debugging. It does not create a git tag,
push commits, publish a GitHub release, or deploy alpha.

Use the guarded alpha deploy script after pushing a release tag:

```sh
CMESH_VERSION=v0.1.0-alpha.44 scripts/deploy-alpha.sh
```

The script checks every release asset used by the manager invite page before touching the VPS. It refuses to deploy while GitHub is still publishing desktop installers, which prevents broken download links such as a missing macOS DMG.

To check release readiness without deploying:

```sh
CMESH_VERSION=v0.1.0-alpha.44 CMESH_DRY_RUN=true scripts/deploy-alpha.sh
```
