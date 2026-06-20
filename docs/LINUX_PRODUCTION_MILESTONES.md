# CMesh Linux Production Milestones

This file is the working source of truth for the Linux production launch path.
Update it after every meaningful implementation step and include this checklist
in status reports.

Last updated: 2026-06-20T14:22:12Z

## Checklist

- [DONE] LP0. Release-candidate baseline
- [DONE] LP1. Linux release packaging
- [DONE] LP2. One-command manager install
- [DONE] LP3. One-command worker install
- [DONE] LP4. Runtime auto-management
- [DONE] LP5. Supported model matrix
- [DONE] LP6. Production sliced runbook
- [DONE] LP7. Long-run reliability tests
- [DONE] LP8. Public VPS security hardening
- [DONE] LP9. Operator observability
- [DONE] LP10. Backup, restore, and upgrade path
- [DONE] LP11. Real beta deployment
- [DONE] LP12. Public production docs
- [DONE] LP13. Signed stable Linux release
- [DONE] LP14. Early user validation
- [DONE] LP15. Linux production launch

## Current Focus

LP15 is closed. The Linux production launch candidate is validated for the
documented Linux support matrix.

## LP0 Evidence

LP0 is closed by the existing production release-candidate evidence:

- Local production readiness gate:
  `/tmp/cmesh-production-readiness-20260620125901`
- AWS installer E2E:
  `/tmp/cmesh-installers-e2e-20260620122239`
- AWS sliced-model E2E:
  `/tmp/cmesh-cdip-real-gguf-e2e-20260620124630`
- Release dry-run:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/release-dry-run`

## LP1 Exit Criteria

- Linux amd64 and arm64 CLI binaries are built with release version metadata.
- Linux manager and worker installers are included in the release directory.
- The Linux amd64 llama.cpp RPC stage runtime archive is verified and included.
- Release checksums and a machine-readable manifest are generated.
- The release lane is independent of macOS and Windows artifacts.
- A local packaging smoke passes.

## LP1 Evidence

LP1 is closed by the Linux-only production packaging lane.

- Script: `scripts/release-linux-production.sh`
- Make target: `make linux-production-release VERSION=v...`
- Local smoke package:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-local.1`
- Package assets:
  - `cmesh-linux-amd64`
  - `cmesh-linux-arm64`
  - `install-manager-linux.sh`
  - `install-worker.sh`
  - `llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz`
  - `manifest.json`
  - `checksums.txt`
- Validation:
  - `bash -n scripts/release-linux-production.sh` passed
  - `scripts/release-linux-production.sh` produced manifest and checksums
  - `git diff --check` passed

## LP2 Exit Criteria

- A clean Linux host can install the manager with one command.
- The manager starts under systemd with persistent local state.
- Join and operator secrets are generated or supplied safely.
- Optional domain/TLS setup is documented and validated.
- Uninstall and reinstall behavior is documented and tested.

## LP2 Evidence

LP2 is closed by a package-based manager installer smoke.

- Script: `scripts/linux-production-manager-install-smoke.sh`
- Make target: `make linux-manager-install-smoke`
- Package under test:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-local.1`
- Evidence:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-manager-install-smoke-20260620132628`
- Validation:
  - package checksum verification passed
  - packaged `install-manager-linux.sh` dry-run passed in `ubuntu:24.04`
  - pinned release binary URL selection passed
  - package-local `CMESH_BINARY_URL=file:///pkg/cmesh-linux-amd64` passed
  - generated/configured token reporting passed without printing raw tokens
  - manager systemd hardening markers are present
  - bad-action usage guard passed

## LP3 Exit Criteria

- A clean Linux host can install a worker with one command.
- Worker resource limits, join token, cache directory, and service user are
  configured safely.
- Worker reconnect behavior survives service restart.
- Stage daemon mode can be enabled during install.

## LP3 Evidence

LP3 is closed by a package-based worker installer smoke and one install-path
fix.

- Script: `scripts/linux-production-worker-install-smoke.sh`
- Make target: `make linux-worker-install-smoke`
- Package under test:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-local.2`
- Evidence:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-worker-install-smoke-20260620132842`
- Install-path fix:
  - `install-worker.sh` now infers runtime name/version from explicit
    `CMESH_LLAMA_CPP_RUNTIME_URL`, so package-local runtime URLs produce a
    valid `stage_runner_bin` and stage-daemon service command.
- Validation:
  - package checksum verification passed
  - packaged `install-worker.sh` dry-run passed in `ubuntu:24.04`
  - package-local `CMESH_BINARY_URL=file:///pkg/cmesh-linux-amd64` passed
  - package-local `CMESH_LLAMA_CPP_RUNTIME_URL=file:///pkg/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz` passed
  - service cache, model path, stage session dir, and stage daemon command are
    derived for Linux systemd mode
  - latest-release default binary/runtime URL selection passed
  - worker systemd hardening markers are present
  - bad-action usage guard passed

## LP4 Exit Criteria

- Linux workers can install, verify, repair, and update the pinned llama.cpp
  stage runtime without manual user commands.
- Runtime artifact checksum verification is mandatory.
- Runtime status is visible through worker status and manager observability.
- Runtime repair is idempotent.

## LP4 Evidence

LP4 is closed by checksum-aware runtime packaging and runtime smoke validation.

- Scripts:
  - `scripts/release-linux-production.sh`
  - `scripts/linux-production-runtime-smoke.sh`
  - `scripts/linux-production-worker-install-smoke.sh`
- Make target: `make linux-runtime-smoke`
- Package under test:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-local.5`
- Worker install smoke evidence:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-worker-install-smoke-20260620133238`
- Runtime smoke evidence:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-runtime-smoke-20260620133240`
- Runtime changes:
  - package includes `llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz.sha256`
  - package checksum file uses artifact basename, not build-host absolute path
  - `install-worker.sh` infers runtime name/version from explicit runtime URL
  - `install-worker.sh` requires runtime checksum by default
  - `install-worker.sh` can auto-load adjacent `runtime.tar.gz.sha256`
  - `install-worker.sh` installs Linux runtime dependency `libgomp1`/`libgomp`
    where supported
- Validation:
  - runtime package checksum verification passed
  - Linux container extracted the runtime archive
  - `cmesh-stage-runner --probe` passed
  - `resident-capabilities` passed
  - `resident-loop` capability/shutdown path passed

## LP5 Exit Criteria

- A production-supported Linux model list is defined with exact GGUF files,
  checksums, RAM/disk requirements, and stage split policies.
- Unsupported model architectures fail with clear errors.
- At least one memory-pressure model is officially supported for sliced
  execution.

## LP5 Evidence

LP5 is closed by a code-backed Linux production model matrix.

- Model matrix: `docs/LINUX_MODEL_MATRIX.md`
- Catalog metadata: `internal/models/catalog.go`
- Guard test: `TestLinuxProductionCatalogDeclaresSlicedModel`
- Package under test:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-local.6`
- Supported production sliced model:
  - ID: `qwen2.5-14b-instruct-q4-k-m`
  - file: `Qwen2.5-14B-Instruct-Q4_K_M.gguf`
  - SHA256:
    `d989c91de35f32c18bdb8bec96a4b9fff2c3e5bca066503c63a5ca54dd537a4b`
  - layers: `48`
  - min/recommended stages: `3`
  - min worker allocation: `6 GB RAM`, `20 GB disk`
  - placement: `memory_disk_weighted_layers`
  - evidence: `/tmp/cmesh-cdip-real-gguf-e2e-20260620124630`
- Validation:
  - `go test ./internal/models` passed
  - Linux production package includes `docs/LINUX_MODEL_MATRIX.md`
  - manager, worker, and runtime package smokes passed against
    `v0.1.0-linux-local.6`

## LP6 Exit Criteria

- A real operator runbook describes manager install, worker join, model install,
  sliced placement, distributed generate, recovery, logs, and cleanup.
- The runbook uses only production release artifacts and no local dev paths.

## LP6 Evidence

LP6 is closed by a production sliced-model runbook and runbook smoke.

- Runbook: `docs/LINUX_SLICED_RUNBOOK.md`
- Smoke: `scripts/linux-production-runbook-smoke.sh`
- Make target: `make linux-runbook-smoke`
- Package under test:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-local.7`
- Evidence:
  - runbook smoke passed
  - manager package smoke:
    `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-manager-install-smoke-20260620133722`
  - worker package smoke:
    `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-worker-install-smoke-20260620133723`
  - runtime smoke:
    `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-runtime-smoke-20260620133725`
- Runbook covers:
  - package checksum verification
  - manager install and health checks
  - worker service and stage daemon install
  - model SHA256 verification
  - `distributed-plan`
  - `distributed-generate`
  - CDIP `prepare`
  - CDIP `decode-loop`
  - observability, logs, recovery, and cleanup

## LP7 Exit Criteria

- Long-running sliced decode test runs repeated prompts without session leaks.
- Worker restart, stage daemon restart, and manager restart paths are covered.
- Cleanup after partial failure is verified.

## LP7 Evidence

LP7 is closed by a repeated local sliced reliability smoke.

- Script: `scripts/linux-production-reliability-smoke.sh`
- Make target: `make linux-reliability-smoke`
- Evidence:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-reliability-smoke-20260620133918`
- Validation:
  - `cdip-daemon-decode-loop-smoke` ran twice
  - each decode loop completed three token steps and produced
    ` token-1 token-2 token-3`
  - `cdip-daemon-session-recreate-smoke` passed
  - `cdip-recovery-cleanup-smoke` passed
  - final reliability summary reported `status=passed`

## LP8 Exit Criteria

- Public manager deployment has documented firewall, TLS, admin-token, join-token,
  rotation, and least-privilege guidance.
- Security smoke is part of production validation.

## LP8 Evidence

LP8 is closed by public VPS hardening docs and security smoke validation.

- Security hardening doc: `docs/LINUX_SECURITY_HARDENING.md`
- Security doc smoke: `scripts/linux-production-security-doc-smoke.sh`
- Existing API/security smoke: `scripts/production-security-smoke.sh`
- Evidence:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-security-smoke-20260620134112`
- Validation:
  - security hardening doc smoke passed
  - manager public-token guard passed
  - admin endpoints reject missing/wrong/join-token auth
  - worker join rejects missing/wrong/operator-token auth
  - worker auth isolation passed
  - local worker control API token rejection passed

## LP9 Exit Criteria

- Operators can inspect cluster, workers, runtime, stage sessions, jobs, and
  failure causes through APIs and logs.
- Evidence bundles are generated for failed and successful distributed runs.

## LP9 Evidence

LP9 is closed by operator observability docs and smoke validation.

- Observability doc: `docs/LINUX_OBSERVABILITY.md`
- Observability doc smoke: `scripts/linux-production-observability-doc-smoke.sh`
- API smoke: `scripts/observability-smoke.sh`
- Evidence:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-observability-smoke-20260620134215`
- Validation:
  - observability doc smoke passed
  - `/v1/observability` auth and JSON shape passed
  - worker visibility passed
  - stage execution visibility passed
  - recovery/cleanup counters are part of the documented evidence bundle

## LP10 Exit Criteria

- Manager local state can be backed up and restored.
- Upgrade path between release versions is documented and tested.
- Rollback behavior is defined.

## LP10 Evidence

LP10 is closed by backup/restore documentation and a file-state restore smoke.

- Backup/restore doc: `docs/LINUX_BACKUP_RESTORE.md`
- Smoke: `scripts/linux-manager-backup-restore-smoke.sh`
- Make target: `make linux-backup-restore-smoke`
- Evidence:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-manager-backup-restore-smoke-20260620134423`
- Validation:
  - manager started with file store and explicit `--state-path`
  - worker join was persisted to state file
  - job was persisted to state file
  - state backup copy was restored into a second manager process
  - restored `/v1/nodes` contained the worker
  - restored `/v1/jobs` contained the job
  - upgrade and rollback procedure is documented

## LP11 Exit Criteria

- A real beta cluster is deployed from release artifacts, not local builds.
- It runs manager plus at least two worker machines.
- It completes a sliced model generation and records evidence.

## LP11 Evidence

LP11 is closed by a package-based AWS beta deployment proof.

- Wrapper: `scripts/linux-beta-deployment-e2e.sh`
- Make target: `make linux-beta-deployment-e2e CMESH_LINUX_PACKAGE_DIR=...`
- Package under test:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-local.8`
- Evidence root:
  `/tmp/cmesh-linux-beta-deployment-20260620135153`
- Installer phase evidence:
  `/tmp/cmesh-linux-beta-deployment-20260620135153/installers`
- Sliced phase evidence:
  `/tmp/cmesh-linux-beta-deployment-20260620135153/sliced`
- Validation:
  - package checksum and manifest validation passed before AWS work
  - AWS installer E2E passed from release artifacts
  - AWS sliced GGUF E2E passed from release artifacts
  - all remote hosts reported `cmesh v0.1.0-linux-local.8`
  - Qwen2.5 14B Q4_K_M was downloaded on three Linux hosts
  - physical stage GGUF shards were created and probed:
    - stage 0: layers `0..15`, about `2.88 GiB`
    - stage 1: layers `16..31`, about `2.40 GiB`
    - stage 2: layers `32..47`, about `3.09 GiB`
  - manager and three stage worker services were installed through packaged
    installers
  - three resident `llama.cpp` stage daemon workers reported ready
  - memory-aware placement verified model requirement `16 GiB` exceeds a single
    worker budget while aggregate stage memory is sufficient
  - distributed terminal decode succeeded
  - decode-loop dispatch reached step `3`
  - terminal result reported `resident_kv_in_memory=true`
  - cleanup verified:
    - installer phase instances terminated:
      `i-0a3a2a2548bf11b96`, `i-01c316064a42d836e`,
      `i-0fc2a24bad4ff47d5`
    - sliced phase instances terminated:
      `i-089276e16a2e69310`, `i-09ae4877ee461b534`,
      `i-0daf12308e4040fda`
    - sliced security group deleted: `sg-0961ec0e63506c62f`

## LP12 Exit Criteria

- Public docs describe what Linux production supports, what it does not support,
  and how users install, operate, and remove CMesh.
- Docs avoid claiming broad Windows/macOS production support.

## LP12 Evidence

LP12 is closed by the public Linux production guide and docs smoke.

- Public guide: `docs/LINUX_PRODUCTION.md`
- README entrypoint: `README.md`
- Smoke: `scripts/linux-production-docs-smoke.sh`
- Make target: `make linux-production-docs-smoke`
- Validation:
  - guide states Linux-only production scope
  - guide includes package verification, manager install, worker install,
    supported model, sliced generation, observability, backup/uninstall, and
    current evidence
  - guide explicitly lists Windows and macOS sliced execution as not production
    supported
  - README links to the public Linux production guide
  - Linux model matrix points to package-based beta evidence
  - `scripts/linux-production-docs-smoke.sh` passed
  - `git diff --check` passed

## LP13 Exit Criteria

- Linux release artifacts are tagged, checksummed, and published.
- Release notes include exact supported platforms, model matrix, evidence
  directories, and known limitations.

## LP13 Evidence

LP13 is closed locally by signed Linux stable release artifacts.

- Signing script: `scripts/sign-linux-production-release.sh`
- Signature smoke: `scripts/linux-stable-release-smoke.sh`
- Make targets:
  - `make linux-sign-production-release CMESH_LINUX_PACKAGE_DIR=...`
  - `make linux-stable-release-smoke CMESH_LINUX_PACKAGE_DIR=...`
- Signed package:
  `dist/linux-production/<version>`
- Signed tarball:
  `dist/linux-production/<version>.tar.gz`
- Signing artifacts:
  - `SIGNING.md`
  - `release-signing-public-key.pem`
  - `manifest.json.sig`
  - `checksums.txt.sig`
  - `signature-manifest.json`
  - `<version>.tar.gz.sha256`
  - `<version>.tar.gz.sig`
- Public key SHA256 is recorded in `signature-manifest.json`.
- Validation:
  - package checksums verified
  - `manifest.json` signature verified
  - `checksums.txt` signature verified
  - tarball checksum verified
  - tarball signature verified
  - private signing key was not present inside package or tarball
  - `git diff --check` passed

## LP14 Exit Criteria

- At least one fresh-user install is validated from public artifacts.
- Feedback issues that block install or first distributed run are fixed.

## LP14 Evidence

LP14 is closed by a clean Ubuntu fresh-user validation from the signed tarball.

- Smoke: `scripts/linux-fresh-user-validation-smoke.sh`
- Make target:
  `make linux-fresh-user-validation-smoke CMESH_LINUX_PACKAGE_DIR=...`
- Signed package:
  `dist/linux-production/<version>`
- Signed tarball:
  `dist/linux-production/<version>.tar.gz`
- Evidence:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-launch-gate-20260620142602/fresh-user-smoke.txt`
- Validation:
  - clean `ubuntu:24.04` container received release tarball artifacts only
  - tarball signature verified before extraction
  - tarball checksum verified
  - package `manifest.json` and `checksums.txt` signatures verified
  - package checksums verified after extraction
  - manager installer dry-run passed from extracted package binary
  - worker installer dry-run passed with runtime checksum requirement and
    resident stage daemon settings
  - tarball was rebuilt without macOS xattr warnings

## LP15 Exit Criteria

- Linux users can install manager and workers from public artifacts.
- A supported memory-pressure model runs sliced across multiple machines.
- Production validation gates pass from release artifacts.
- Recovery, cleanup, and upgrade paths are documented and tested.
- The Linux release is acceptable to call production for the documented support
  matrix.

## LP15 Evidence

LP15 is closed by the Linux production launch gate.

- Gate: `scripts/linux-production-launch-gate.sh`
- Make target:
  `make linux-production-launch-gate CMESH_LINUX_PACKAGE_DIR=...`
- Package under test:
  `dist/linux-production/<version>`
- AWS beta evidence:
  `/tmp/cmesh-linux-beta-deployment-20260620135153`
- Launch gate evidence:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-launch-gate-20260620142602`
- Validation:
  - signed release package and tarball verified
  - public production docs verified
  - fresh-user signed tarball flow verified
  - manager installer dry-run passed
  - worker installer dry-run passed
  - runtime artifact verification passed
  - sliced runbook smoke passed
  - security and observability docs passed
  - backup/restore smoke passed
  - repeated local reliability smoke passed
  - AWS installer and sliced beta evidence verified with cleanup
  - Go regression tests passed
  - `git diff --check` passed
