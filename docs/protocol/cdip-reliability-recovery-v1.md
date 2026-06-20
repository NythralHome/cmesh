# CDIP Reliability And Recovery V1

Status: production-readiness contract for CMesh alpha.

This document defines the reliability behavior CMesh must preserve for
distributed/layer-stage execution. It is intentionally concrete: these rules are
what manager, worker, and stage daemon code should do today.

## Scope

Covered:

- worker heartbeat loss
- stale scheduled/running jobs
- stage daemon session loss
- parent/stage cancellation
- late worker completion
- resident stage session cleanup

Not covered:

- Byzantine workers
- cryptographic attestation
- cross-manager consensus recovery
- preserving native in-memory KV after a daemon process restart

## Failure Classes

### Retryable

Retryable failures may be rescheduled if the job still has attempts remaining.

- worker heartbeat timeout
- worker explicitly leaves or is marked offline
- worker job error before max attempts is reached
- stale scheduled/running job with no progress for the configured stale window
- stage daemon session missing during decode, when daemon itself is reachable

### Fatal

Fatal failures complete the job as failed and should propagate to the parent
distributed job when the failed job is a CDIP stage.

- max attempts exhausted
- unsupported job type
- invalid CDIP transition
- invalid activation checksum
- stage daemon session recreate fails
- stage daemon decode returns non-404 error
- model shard/runtime is unavailable after retry budget is exhausted

### Canceled

Cancellation is operator-driven or parent-driven. It is terminal and must be
idempotent.

- canceling a distributed parent cancels non-terminal stage jobs
- canceling a stage job should not revive or complete the parent
- late worker completion for a canceled job must keep the job canceled
- resident daemon session cleanup is best-effort and must not block the cancel
  response

## Manager Responsibilities

The manager owns durable job state and recovery decisions.

Required behavior:

- scheduled/running jobs assigned to an offline worker are rescheduled to a
  different capable worker when possible
- if no different capable worker exists, the job returns to queued state and
  records the reason
- if attempts are exhausted, the job becomes failed
- if a failed job is a CDIP stage, its parent distributed job becomes failed
- stale scheduled/running jobs are recovered by the background job recovery loop
- stale CDIP stages with resident daemon sessions trigger best-effort
  `DELETE /v1/sessions/{session_id}` cleanup
- parent cancellation cascades to non-terminal CDIP stage jobs
- parent cancellation triggers best-effort resident session cleanup for those
  stages

The manager must not assume that daemon metadata means native in-memory KV still
exists after daemon restart.

## Worker Responsibilities

Workers execute assigned jobs and report completion once.

Required behavior:

- a worker must reject or fail a job it cannot execute instead of silently
  hanging
- worker progress updates should refresh job activity timestamps
- if stage daemon decode returns `404`, worker recreates the deterministic stage
  session and retries decode once
- only missing-session decode is retried automatically; other daemon errors are
  returned as job errors
- worker completion for an old assignment must not complete a job that has been
  reassigned to another worker

## Stage Daemon Responsibilities

The stage daemon owns per-stage resident sessions.

Required behavior:

- `POST /v1/sessions` creates or replaces a deterministic session id
- `GET /v1/sessions/{id}` returns session metadata or `404`
- `POST /v1/sessions/{id}/decode` returns `404` when the session is missing
- `DELETE /v1/sessions/{id}` closes backend state, removes persisted metadata,
  and is safe to call during recovery
- `/health` reports session count, backend kind, and backend readiness

The stage daemon may persist metadata to disk for audit and recovery evidence,
but it must not claim native in-memory model/KV survived a process restart.

## Attempt Accounting

CMesh counts attempts when a job is assigned for execution.

Rules:

- queued jobs have `attempts == 0` until scheduled
- scheduling increments attempts
- rescheduling to another worker increments attempts
- stale recovery does not immediately reschedule back to the same stuck worker
- max attempts exhaustion is terminal

## Required Evidence

M11 reliability is not considered complete unless these pass:

- `go test ./...`
- `scripts/cdip-recovery-cleanup-smoke.sh`
- `scripts/cdip-daemon-session-recreate-smoke.sh`
- `bash -n scripts/*.sh`
- `git diff --check`

`scripts/production-readiness-gate.sh` must include the recovery cleanup and
session recreate smokes before any AWS proof is treated as production evidence.

## Current Guardrail

CMesh has production-shaped recovery for the CDIP manager/worker/stage-daemon
control plane. This does not yet mean native llama.cpp sliced inference is
production complete. The remaining native-runtime gap is preserving real
per-stage llama.cpp model/context/KV ownership inside a resident process across
token steps.
