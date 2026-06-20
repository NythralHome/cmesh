# CMesh v0.1.0 Linux RC1 Announcement Draft

CMesh v0.1.0 Linux RC1 is the first public release candidate for CMesh's
Linux-first distributed AI compute path.

CMesh is a private AI compute cluster manager. It lets an operator install a
Linux manager, join Linux worker machines with explicit resource limits, verify
runtime artifacts, and run the documented production-like sliced model flow for
the supported Linux model matrix.

## What Is New

- Signed Linux release tarball with manifest, checksums, and detached
  signatures.
- One-command Linux manager installer.
- One-command Linux worker installer.
- Pinned and checksum-verified Linux amd64 llama.cpp stage runtime.
- Supported model matrix for `qwen2.5-14b-instruct-q4-k-m`.
- Physical GGUF stage shard flow for the supported model.
- AWS evidence for package-based manager/worker install and sliced execution.
- Fresh-user validation from the signed tarball in `ubuntu:24.04`.
- Public docs for install, sliced runbook, security hardening, observability,
  backup/restore, signing, and governance.

## What It Does Not Claim

- It does not support Windows or macOS production installs yet.
- It does not support arbitrary model slicing.
- It does not provide a public trustless worker marketplace.
- It does not provide payments, credits, reputation, or fraud resistance.
- It does not make sensitive prompts safe on untrusted worker machines.

## Try It

Download the release assets from GitHub:

- `v0.1.0-linux-rc.1.tar.gz`
- `v0.1.0-linux-rc.1.tar.gz.sha256`
- `v0.1.0-linux-rc.1.tar.gz.sig`
- `v0.1.0-linux-rc.1.tar.gz.public-key.pem`

Follow:

- `README.md`
- `docs/LINUX_PRODUCTION.md`
- `docs/LINUX_SLICED_RUNBOOK.md`

## Contribute

Useful early contributions:

- test the Linux install flow on clean VPS providers;
- report installer/runtime failures with logs and tokens redacted;
- improve runbooks and troubleshooting;
- add model-matrix candidates with exact GGUF checksums and resource evidence;
- help design Windows/macOS production parity;
- review the CDIP sliced execution protocol and runtime boundaries.

Before contributing, read:

- `CONTRIBUTING.md`
- `SECURITY.md`
- `docs/RELEASE_SCOPE.md`
- `docs/THIRD_PARTY_NOTICES.md`

## Roadmap After RC1

- v0.1.1 Linux stabilization and bug fixes.
- Better diagnostics for installer/runtime failure modes.
- More reliability and recovery checks for long-running stage daemons.
- Windows/macOS production parity planning.
- Broader model matrix only after exact checksums, resource requirements, and
  sliced execution evidence exist.
