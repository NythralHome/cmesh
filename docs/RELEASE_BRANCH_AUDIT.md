# CMesh Release Branch Audit

This audit defines how to turn the current working tree into a public release
branch without reverting unrelated user work. It is intentionally conservative:
the release branch should contain only source, scripts, docs, and workflow files
required to reproduce and validate the Linux public release.

## Branch Strategy

- Create a release branch from the current development branch after R2 is
  accepted.
- Recommended branch name: `release/v0.1-linux`.
- Do not commit `dist/`, `/tmp`, local evidence directories, private signing
  keys, or local generated binaries.
- Commit release scripts, installer scripts, docs, tests, and source changes
  required by the Linux production launch gate.
- Build final release artifacts from the release branch, then sign them with
  the real release key in R3/R4.

## Release-Critical Source Changes

These modified paths are expected to be part of the release branch because they
define manager/worker behavior, CDIP sliced execution, runtime integration,
model catalog, persistence, or tests:

- `cmd/cmesh/main.go`
- `cmd/cmesh/main_test.go`
- `cmd/cmesh/model_jobs_test.go`
- `internal/cdip/types.go`
- `internal/cdip/types_test.go`
- `internal/cluster/types.go`
- `internal/manager/cdip.go`
- `internal/manager/distributed.go`
- `internal/manager/distributed_test.go`
- `internal/manager/file_store.go`
- `internal/manager/file_store_test.go`
- `internal/manager/postgres_store.go`
- `internal/manager/server.go`
- `internal/manager/server_test.go`
- `internal/manager/state.go`
- `internal/manager/store.go`
- `internal/membership/types.go`
- `internal/models/catalog.go`
- `internal/models/catalog_test.go`
- `internal/protocol/distributed_rpc.go`
- `internal/protocol/distributed_rpc_test.go`
- `internal/resources/discovery.go`
- `internal/resources/discovery_test.go`
- `internal/runtimes/distributed_stage.go`
- `internal/runtimes/distributed_stage_test.go`
- `internal/runtimes/llamacpp.go`
- `internal/runtimes/llamacpp_stage.go`
- `internal/runtimes/llamacpp_stage_test.go`
- `internal/runtimes/llamacpp_test.go`
- `internal/runtimes/stage_adapter.go`

## Release-Critical Scripts

These scripts are expected release branch content:

- `scripts/install-manager-linux.sh`
- `scripts/install-worker.sh`
- `scripts/release-linux-production.sh`
- `scripts/sign-linux-production-release.sh`
- `scripts/init-release-signing-key.sh`
- `scripts/linux-production-launch-gate.sh`
- `scripts/linux-stable-release-smoke.sh`
- `scripts/linux-fresh-user-validation-smoke.sh`
- `scripts/linux-beta-deployment-e2e.sh`
- `scripts/aws-installers-e2e.sh`
- `scripts/aws-cdip-real-gguf-e2e.sh`
- `scripts/linux-production-manager-install-smoke.sh`
- `scripts/linux-production-worker-install-smoke.sh`
- `scripts/linux-production-runtime-smoke.sh`
- `scripts/linux-production-runbook-smoke.sh`
- `scripts/linux-production-reliability-smoke.sh`
- `scripts/linux-production-security-doc-smoke.sh`
- `scripts/linux-production-observability-doc-smoke.sh`
- `scripts/linux-manager-backup-restore-smoke.sh`
- `scripts/linux-production-docs-smoke.sh`
- `scripts/build-llamacpp-runtime-linux-docker.sh`
- `scripts/prepare-current-stage-runtime-artifact.sh`
- `scripts/verify-llamacpp-runtime-artifact.sh`
- `scripts/verify-llamacpp-stage-loader-patch.sh`

## Release-Critical Docs

These docs are expected release branch content:

- `README.md`
- `docs/LINUX_PRODUCTION.md`
- `docs/LINUX_PRODUCTION_MILESTONES.md`
- `docs/LINUX_MODEL_MATRIX.md`
- `docs/LINUX_SLICED_RUNBOOK.md`
- `docs/LINUX_SECURITY_HARDENING.md`
- `docs/LINUX_OBSERVABILITY.md`
- `docs/LINUX_BACKUP_RESTORE.md`
- `docs/RELEASE_MILESTONES.md`
- `docs/RELEASE_SCOPE.md`
- `docs/RELEASE_BRANCH_AUDIT.md`
- `docs/protocol/distributed-inference-v1.md`
- `docs/protocol/layer-sharding-research.md`
- `docs/protocol/llama-stage-adapter-spike.md`

## Release Support Files

These files should be included if the release branch uses them:

- `.github/workflows/release.yml`
- `Makefile`
- `scripts/README.md`
- `SECURITY.md`
- `CONTRIBUTING.md`
- `CODE_OF_CONDUCT.md`
- `LICENSE`
- `NOTICE`

## Generated Or Local-Only Files

These should not be committed to the release branch:

- `dist/`
- `bin/`
- local runtime cache directories
- local AWS evidence under `/tmp`
- local launch-gate evidence under `/var/folders/...`
- private signing keys
- generated package tarballs and signatures before the final GitHub release

## Items To Review Before Tagging

- Confirm `.gitignore` excludes local release output and signing private keys.
- Confirm no absolute `/Volumes/...`, `/tmp/...`, or `/var/folders/...` paths
  remain in public docs except milestone evidence files.
- Confirm `SECURITY.md` has a real reporting process.
- Confirm release notes do not claim Windows/macOS production support.
- Confirm final package is signed with a real release key, not the generated
  test key used by local validation.
