# CMesh v0.1 Linux Public Release Scope

This release freezes the first public CMesh scope around the validated Linux
production launch candidate. It is intentionally narrow: the goal is to let a
new Linux operator install a manager, join Linux workers, verify runtime
artifacts, and run the documented production-like sliced model flow.

## Supported

- Platform: Linux hosts only.
- Architectures:
  - manager/worker CLI: Linux amd64 and arm64 release binaries.
  - llama.cpp stage runtime: Linux amd64.
- Install path:
  - one-command manager install through `install-manager-linux.sh`.
  - one-command worker install through `install-worker.sh`.
  - systemd services for manager and worker.
  - package-local install flow from signed release tarball.
- Persistence:
  - local manager persistence as documented in the Linux production docs.
  - backup and restore runbook for manager state.
- Runtime:
  - pinned llama.cpp stage runtime artifact.
  - checksum-verified runtime install and repair path.
  - resident stage daemon path for sliced execution.
- Distributed execution:
  - CDIP sliced/layer-stage execution path for the supported Linux model
    matrix.
  - memory-aware placement proof for a model that exceeds a single worker's
    declared memory allocation.
  - production-like sliced model run validated through local and AWS evidence.
- Supported model matrix:
  - `qwen2.5-14b-instruct-q4-k-m`
  - GGUF SHA256:
    `d989c91de35f32c18bdb8bec96a4b9fff2c3e5bca066503c63a5ca54dd537a4b`
  - 48 layers, 3 recommended stages, memory/disk requirements documented in
    `docs/LINUX_MODEL_MATRIX.md`.
- Security and operations:
  - public VPS hardening guidance.
  - operator observability runbook.
  - backup, restore, and upgrade guidance.
  - signed package, signed manifest, signed checksums, and signed tarball.

## Not Supported In This Release

- Windows production install.
- macOS production install.
- GPU acceleration as a production-supported path.
- Multiple public model architectures beyond the documented Linux matrix.
- Public hosted CMesh control plane.
- Token billing, payments, or public marketplace features.
- Fully decentralized trustless membership.
- Arbitrary Hugging Face model installation without model-matrix validation.
- Claims that every model can be split across arbitrary machines.

## Release Evidence

- Linux production milestones:
  `docs/LINUX_PRODUCTION_MILESTONES.md`
- Final Linux launch gate:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-launch-gate-20260620142602`
- AWS beta evidence:
  `/tmp/cmesh-linux-beta-deployment-20260620135153`
- Current signed local release candidate:
  `/Volumes/Devspace/Projects/CMesh/cmesh/dist/linux-production/v0.1.0-linux-local.11.tar.gz`

## Release Notes Boundary

The public release should say:

CMesh v0.1 is a Linux-first release candidate for running a production-like
sliced AI model flow across multiple Linux workers. It includes a manager,
worker installer, runtime verification, supported model matrix, sliced runbook,
signed release artifacts, fresh-user validation, and AWS cleanup-verified beta
evidence.

The public release should not say:

- CMesh supports every model.
- CMesh supports Windows/macOS production installs.
- CMesh is a trustless public marketplace.
- CMesh can automatically split any arbitrary model with no model-specific
  validation.
