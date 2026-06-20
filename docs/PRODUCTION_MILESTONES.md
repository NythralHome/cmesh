# CMesh Production Milestones

This file is the working source of truth for the path to a production release
candidate. Keep it updated after every meaningful implementation step.

Last updated: 2026-06-20T13:02:30Z

## Checklist

- [DONE] M0. Product direction locked
- [DONE] M1. Basic cluster foundation
- [DONE] M2. Local model execution
- [DONE] M3. Distributed RPC baseline
- [DONE] M4. CDIP protocol scaffold
- [DONE] M5. Physical stage artifacts
- [DONE] M6. Local sliced execution proof
- [DONE] M7. Resident stage runtime
- [DONE] M8. Multi-worker sliced execution
- [DONE] M9. Memory-aware placement
- [DONE] M10. Real AWS proof for memory-pressure model
- [DONE] M11. Reliability and recovery
- [DONE] M12. Security hardening
- [DONE] M13. Installer production path
- [DONE] M14. Observability
- [DONE] M15. Production release candidate

## Current Focus

M15 is closed: production release candidate evidence is available.

The next work after this milestone file is to cut an actual tagged RC from the
validated tree, publish artifacts, and keep extending the sliced execution path
from release-candidate quality toward broader model/runtime coverage.

## M10 Evidence

M10 is closed by a real AWS proof that ran a memory-pressure model across three
`t3.large` instances and then cleaned up all AWS resources.

- Evidence directory: `/tmp/cmesh-cdip-real-gguf-e2e-20260620114339`
- Model: `qwen2.5-14b-instruct-q4-k-m`
- Model file: `Qwen2.5-14B-Instruct-Q4_K_M.gguf`
- Layers: 48
- Stage layout: 3 stages, 16 layers each
- Stage shards:
  - stage 0: layers 0-15
  - stage 1: layers 16-31
  - stage 2: layers 32-47
- Placement:
  - `feasible=true`
  - `executable_now=true`
  - `memory_pressure_placement_verified=true`
  - `memory_pressure_execution_verified=true`
  - `resident_stage_workers=3`
- Execution:
  - `execution_mode=resident-stage-daemon`
  - `runner_mode=llama.cpp-stage-daemon`
  - `resident_kv_in_memory=true`
  - dispatch parent job: `job-02431ed53100434b`
  - dispatch status: `succeeded`
  - dispatch result text: `Hello`
- Cleanup:
  - instances terminated: `i-0bafaa0e437493dec`, `i-0df63e261acdf0cb3`, `i-090193b3429fc9910`
  - security group deleted: `sg-0a968707884468302`

## M11 Exit Criteria

- Stage daemon restart recovery is tested locally and in an AWS-style fixture.
- Resident session recreate does not lose job lineage or corrupt terminal
  output.
- Activation relay replay or retry is deterministic and bounded.
- Parent job reports precise failure state when recovery is not possible.
- Cleanup scripts can safely stop manager, workers, daemons, and temporary
  artifacts after partial failure.

## M11 Evidence

M11 is closed by local recovery smokes and the local production readiness gate.

- Recovery cleanup evidence:
  `/tmp/cmesh-cdip-recovery-cleanup-smoke-20260620115748`
- Daemon session recreate evidence:
  `/tmp/cmesh-cdip-daemon-session-recreate-smoke-20260620115748`
- Full local production readiness gate:
  `/tmp/cmesh-production-readiness-20260620120953`
- Gate status:
  - `go test ./...` passed
  - `bash -n scripts/*.sh` passed
  - `git diff --check` passed
  - `cdip-recovery-cleanup-smoke` passed
  - `cdip-daemon-session-recreate-smoke` passed
  - `cdip-real-gguf-multi-daemon-worker-smoke` passed
  - `aws-cdip-preflight` passed with the 14B memory-pressure configuration

## M12 Exit Criteria

- Production security smoke covers manager admin access, worker auth, and local
  control API app-token rejection.
- Join tokens can be rotated without leaking static credentials into public
  docs or release artifacts.
- Worker node auth tokens are stored locally with restricted permissions where
  the platform supports it.
- Public manager deployment docs separate admin APIs from worker join APIs.
- Security checks are part of `scripts/production-readiness-gate.sh`.

## M12 Evidence

M12 is closed by the expanded production security smoke and related tests.

- Production security smoke evidence:
  `/tmp/cmesh-production-security-smoke-20260620121718` or platform temp
  equivalent from the latest run.
- Covered checks:
  - public manager refuses to start without join and operator tokens
  - manager admin endpoints reject missing, wrong, and join-token-as-admin auth
  - worker join rejects missing, wrong, and operator-token-as-join auth
  - per-node worker auth tokens do not leak through `/v1/nodes`
  - worker A token cannot heartbeat, poll jobs, or leave worker B
  - local worker control API rejects missing and wrong
    `X-CMesh-Control-Token`
- Validation:
  - `scripts/production-security-smoke.sh` passed
  - `go test ./internal/manager ./cmd/cmesh` passed
  - `go test ./internal/workercontrol` passed
  - `bash -n scripts/*.sh` passed
  - `git diff --check` passed

## M13 Exit Criteria

- Manager installer can provision a fresh Linux host with systemd service,
  persistent local store, operator/join secrets, optional TLS reverse proxy, and
  documented uninstall/reinstall behavior.
- Worker installer can provision a fresh Linux host with systemd service,
  runtime artifact verification, stage daemon mode, resource limits, safe local
  secrets, and reconnect behavior.
- Installer dry-run smoke verifies generated commands and required environment
  without root or network side effects.
- AWS installer smoke provisions real manager/worker hosts, verifies services,
  then terminates all created resources.
- Production readiness gate includes the installer dry-run by default and the
  AWS installer proof when `CMESH_RUN_AWS_E2E=true`.

## M13 Evidence

M13 is closed by installer documentation, dry-run verification, local gate, and
a cautious AWS installer proof.

- Install runbook:
  `docs/PRODUCTION_INSTALL.md`
- Installer dry-run evidence:
  `/tmp/cmesh-production-readiness-20260620122006/installers-dry-run-smoke`
- Full local production readiness gate without AWS:
  `/tmp/cmesh-production-readiness-20260620122006`
- AWS installer E2E evidence:
  `/tmp/cmesh-installers-e2e-20260620122239`
- AWS installer E2E result:
  - manager installed through `install-manager-linux.sh`
  - two workers installed through `install-worker.sh`
  - `/v1/cluster` reported `workers_online=2`
  - `cmesh.service` active on manager
  - `cmesh-worker.service` active on both workers
- AWS cleanup:
  - instances terminated: `i-001a8aae6118083c8`,
    `i-0fecc0ea0e09bd8ac`, `i-0dfb15ecb9a6251f5`
  - security group deleted: `sg-00d8b55104ad179cc`

## M14 Exit Criteria

- `/v1/observability` exposes production-useful cluster, worker, runtime,
  stage-daemon, distributed job, and cleanup/recovery summaries.
- Observability smoke validates auth, JSON shape, worker visibility, stage
  execution visibility, and recovery counters where available.
- Distributed sliced jobs include enough execution trace data to debug
  placement, stage worker assignment, resident runtime status, timings, and
  failure causes.
- Production readiness gate includes observability smoke by default.

## M14 Evidence

M14 is closed by expanded observability JSON and smoke coverage.

- `/v1/observability` now includes:
  - cluster counters
  - per-worker summaries with resource snapshots
  - per-worker stage daemon endpoint, protocol, readiness, hooks, and blockers
  - job counters
  - recent distributed/CDIP job summaries
  - CDIP counters
  - RPC health counters
  - recovery configuration
  - blocker list
- Observability smoke evidence:
  `/tmp/cmesh-observability-smoke-20260620122739` or platform temp equivalent
  from the latest run.
- Validation:
  - `scripts/observability-smoke.sh` passed
  - `go test ./internal/manager ./cmd/cmesh ./internal/workercontrol` passed
  - `bash -n scripts/*.sh` passed
  - `git diff --check` passed

## M15 Exit Criteria

- Full local production readiness gate passes.
- AWS installer proof passes and cleanup is verified.
- AWS sliced-model proof passes with memory-pressure placement and cleanup is
  verified.
- Release dry-run validates expected binaries, runtime artifacts, checksums,
  docs, and release notes.
- Release candidate status report references exact evidence directories and
  known limitations.

## M15 Evidence

M15 is closed by the local production gate, installer proof, release dry-run,
and a final AWS sliced-model proof using the 14B Qwen memory-pressure model.

- Full local production readiness gate:
  `/tmp/cmesh-production-readiness-20260620125901`
- Release dry-run:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/release-dry-run`
- AWS installer E2E:
  `/tmp/cmesh-installers-e2e-20260620122239`
- AWS sliced-model E2E:
  `/tmp/cmesh-cdip-real-gguf-e2e-20260620124630`
- AWS sliced-model proof:
  - model: `qwen2.5-14b-instruct-q4-k-m`
  - file: `Qwen2.5-14B-Instruct-Q4_K_M.gguf`
  - layers: 48
  - instances: 3 x `t3.large`
  - placement: `memory_disk_weighted_layers`
  - physical stage shards:
    - stage 0: layers 0-15
    - stage 1: layers 16-31
    - stage 2: layers 32-47
  - dispatch parent job: `job-b7e7b43ea58e71bb`
  - dispatch status: `succeeded`
  - dispatch result text: `Hello`
  - `memory_pressure_placement_verified=true`
  - `memory_pressure_execution_verified=true`
  - `execution_mode=resident-stage-daemon`
  - `runner_mode=llama.cpp-stage-daemon`
  - `resident_kv_in_memory=true`
- AWS cleanup:
  - terminated instances: `i-09e023da23d4d6117`,
    `i-0feddc290efce3c49`, `i-0e605ff86081e46db`
  - deleted security group: `sg-0f12ea6e7f73060e7`

Known RC boundaries:

- The production sliced path is validated for the current llama.cpp-derived
  CPU stage-daemon runtime and Qwen2.5-style GGUF stage artifacts.
- The planner is memory-aware, but runtime overhead includes llama.cpp
  repacking and must stay conservative for small hosts.
- This RC proves production-like manager/worker install, physical sharding,
  resident stage sessions, multi-token dispatch, observability, security, and
  cleanup; it is not yet a broad compatibility claim for every GGUF
  architecture, quantization, GPU backend, or operating system.
