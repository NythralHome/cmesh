# CMesh v0.1.0 Linux RC1

CMesh v0.1.0 Linux RC1 is the first public release candidate for the CMesh
Linux production path. It packages the manager, worker, verified runtime
artifact, Linux install scripts, model matrix, sliced runbook, signing metadata,
and validation docs into a signed release tarball.

## What Works

- Linux manager install with `install-manager-linux.sh`.
- Linux worker install with `install-worker.sh`.
- Systemd manager and worker service path.
- Runtime auto-management for the pinned Linux amd64 llama.cpp stage runtime.
- Signed package, signed manifest, signed checksums, and signed tarball.
- Fresh-user validation from a signed tarball.
- Supported sliced model matrix for `qwen2.5-14b-instruct-q4-k-m`.
- Production-like sliced execution evidence across multiple Linux workers.
- Memory-aware placement evidence for a model larger than a single worker's
  declared memory allocation.
- Backup/restore, security hardening, observability, and sliced runbook docs.

## Supported Scope

- Manager/worker CLI: Linux amd64 and Linux arm64.
- llama.cpp stage runtime: Linux amd64.
- Production sliced model: `qwen2.5-14b-instruct-q4-k-m`.
- Deployment model: private invited Linux cluster.

## Not Supported Yet

- Windows production installer.
- macOS production installer.
- GPU production path.
- Trustless public worker marketplace.
- Arbitrary unvalidated Hugging Face model slicing.
- Payments, credits, reputation, or fraud resistance.

## Release Assets

Upload these files together:

- `v0.1.0-linux-rc.1.tar.gz`
- `v0.1.0-linux-rc.1.tar.gz.sha256`
- `v0.1.0-linux-rc.1.tar.gz.sig`
- `v0.1.0-linux-rc.1.tar.gz.public-key.pem`

## Verify

```sh
shasum -a 256 -c v0.1.0-linux-rc.1.tar.gz.sha256
openssl dgst -sha256 \
  -verify v0.1.0-linux-rc.1.tar.gz.public-key.pem \
  -signature v0.1.0-linux-rc.1.tar.gz.sig \
  v0.1.0-linux-rc.1.tar.gz
```

Then extract and verify package internals:

```sh
tar -xzf v0.1.0-linux-rc.1.tar.gz
cd v0.1.0-linux-rc.1
openssl dgst -sha256 -verify release-signing-public-key.pem -signature manifest.json.sig manifest.json
openssl dgst -sha256 -verify release-signing-public-key.pem -signature checksums.txt.sig checksums.txt
shasum -a 256 -c checksums.txt
```

## Evidence

- Linux production launch gate:
  `/var/folders/dp/58nxklj177sckhsl4v9p8g500000gn/T/cmesh-linux-production-launch-gate-20260620144800`
- AWS beta evidence:
  `/tmp/cmesh-linux-beta-deployment-20260620135153`

## Known Constraints

This is an RC. The supported path is intentionally narrow and Linux-first. Use
private invited workers only and do not send sensitive prompts to untrusted
workers.
