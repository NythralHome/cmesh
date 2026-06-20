# CMesh Linux Production Guide

This is the public Linux production guide for the current CMesh support matrix.
It describes what is supported, how to install it, how to verify it, and what is
not yet production-supported.

## Supported Production Scope

CMesh production support currently means:

- Platform: `linux/amd64`
- Init system: `systemd`
- Manager topology: one manager service
- Worker topology: three Linux worker services with resident stage daemons
- Runtime: `llama.cpp-b9704-linux-amd64-rpc-stage`
- Sliced model: `qwen2.5-14b-instruct-q4-k-m`
- Model file: `Qwen2.5-14B-Instruct-Q4_K_M.gguf`
- Placement: `memory_disk_weighted_layers`
- Validated path: manager plus three stage workers on AWS `t3.large`

This production claim is intentionally narrow. Other platforms and models may
work experimentally, but they are not part of this Linux production support
matrix until they have exact package metadata, model checksums, sliced execution
evidence, and cleanup evidence.

## What CMesh Does In This Release

CMesh can install a private Linux manager and Linux workers, keep a pinned
`llama.cpp` stage runtime on workers, split the supported GGUF model into
physical stage artifacts, place stages by available memory and disk, and run a
resident multi-stage decode path where stage daemons keep model/KV session state
in memory across token steps.

The validated memory-pressure proof uses a model with a `16 GB` catalog memory
requirement on multiple workers with about `8 GB` each. The model is not loaded
as one full copy per worker for the production proof; each stage receives a
physical stage GGUF shard.

## Production Package

A Linux production package contains:

- `cmesh-linux-amd64`
- `cmesh-linux-arm64`
- `install-manager-linux.sh`
- `install-worker.sh`
- `llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz`
- `llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz.sha256`
- `manifest.json`
- `checksums.txt`
- Linux production docs under `docs/`

Download the public release candidate:

```sh
VERSION=v0.1.0-linux-rc.1
BASE_URL=https://github.com/NythralHome/cmesh/releases/download/$VERSION

curl -fLO "$BASE_URL/$VERSION.tar.gz"
curl -fLO "$BASE_URL/$VERSION.tar.gz.sha256"
curl -fLO "$BASE_URL/$VERSION.tar.gz.sig"
curl -fLO "$BASE_URL/$VERSION.tar.gz.public-key.pem"
```

The expected public asset filenames are:

- `v0.1.0-linux-rc.1.tar.gz`
- `v0.1.0-linux-rc.1.tar.gz.sha256`
- `v0.1.0-linux-rc.1.tar.gz.sig`
- `v0.1.0-linux-rc.1.tar.gz.public-key.pem`

Verify the tarball before extracting:

```sh
shasum -a 256 -c "$VERSION.tar.gz.sha256"
openssl dgst -sha256 \
  -verify "$VERSION.tar.gz.public-key.pem" \
  -signature "$VERSION.tar.gz.sig" \
  "$VERSION.tar.gz"
```

Extract and verify the package internals before installing:

```sh
tar -xzf "$VERSION.tar.gz"
cd "$VERSION"
openssl dgst -sha256 -verify release-signing-public-key.pem -signature manifest.json.sig manifest.json
openssl dgst -sha256 -verify release-signing-public-key.pem -signature checksums.txt.sig checksums.txt
shasum -a 256 -c checksums.txt
shasum -a 256 -c llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz.sha256
```

For package-local validation during development, run:

```sh
shasum -a 256 -c checksums.txt
shasum -a 256 -c llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz.sha256
```

## Install The Manager

Run on the Linux manager host:

```sh
sudo CMESH_BINARY_URL=file://$PWD/cmesh-linux-amd64 \
  CMESH_NONINTERACTIVE=true \
  CMESH_ADDR=0.0.0.0:18080 \
  CMESH_PUBLIC_URL=http://MANAGER_HOST:18080 \
  CMESH_JOIN_TOKEN=replace-with-generated-secret \
  CMESH_OPERATOR_TOKEN=replace-with-generated-secret \
  ./install-manager-linux.sh install
```

Verify:

```sh
systemctl is-active cmesh.service
curl -fsS http://MANAGER_HOST:18080/health
curl -fsS -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  http://MANAGER_HOST:18080/v1/cluster
```

## Install Workers

Run on each Linux worker host:

```sh
sudo CMESH_BINARY_URL=file://$PWD/cmesh-linux-amd64 \
  CMESH_MANAGER_URL=http://MANAGER_HOST:18080 \
  CMESH_JOIN_TOKEN=replace-with-manager-join-token \
  CMESH_INSTALL_SERVICE=true \
  CMESH_CPU=2 \
  CMESH_MEMORY_GB=6 \
  CMESH_DISK_GB=40 \
  CMESH_STAGE_DAEMON=true \
  CMESH_STAGE_DAEMON_BACKEND=llama.cpp-resident \
  CMESH_LLAMA_CPP_RUNTIME_AUTO=true \
  CMESH_LLAMA_CPP_RUNTIME_URL=file://$PWD/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz \
  CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM=true \
  ./install-worker.sh install
```

Verify on every worker:

```sh
systemctl is-active cmesh-worker.service
systemctl is-active cmesh-stage-daemon.service
curl -fsS http://127.0.0.1:19781/health
```

## Supported Model

Use the exact model from `docs/LINUX_MODEL_MATRIX.md`:

```sh
MODEL_ID=qwen2.5-14b-instruct-q4-k-m
MODEL_FILE=Qwen2.5-14B-Instruct-Q4_K_M.gguf
MODEL_URL=https://huggingface.co/bartowski/Qwen2.5-14B-Instruct-GGUF/resolve/main/Qwen2.5-14B-Instruct-Q4_K_M.gguf
MODEL_SHA256=d989c91de35f32c18bdb8bec96a4b9fff2c3e5bca066503c63a5ca54dd537a4b
```

Verify the model before use:

```sh
curl -fL "$MODEL_URL" -o "$MODEL_FILE"
echo "$MODEL_SHA256  $MODEL_FILE" | shasum -a 256 -c -
```

## Run A Sliced Generation

Check placement:

```sh
curl -fsS -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  http://MANAGER_HOST:18080/v1/models/qwen2.5-14b-instruct-q4-k-m/distributed-plan
```

The plan must show:

- `feasible: true`
- `executable_now: true`
- `placement.strategy: memory_disk_weighted_layers`
- `total_layers: 48`
- three selected workers
- each stage within worker memory and disk limits

Create and run a distributed generation:

```sh
cat > request.json <<'JSON'
{
  "prompt": "Reply with one short greeting.",
  "max_tokens": 3,
  "temperature": "0.1",
  "total_layers": 48,
  "timeout_ms": 300000
}
JSON

curl -fsS -X POST \
  -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  -H "Content-Type: application/json" \
  -d @request.json \
  http://MANAGER_HOST:18080/v1/models/qwen2.5-14b-instruct-q4-k-m/distributed-generate \
  > distributed-generate.json

PARENT_JOB_ID=$(jq -r '.job.id' distributed-generate.json)

curl -fsS -X POST \
  -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{}' \
  http://MANAGER_HOST:18080/v1/cdip/jobs/$PARENT_JOB_ID/prepare

curl -fsS -X POST \
  -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{}' \
  http://MANAGER_HOST:18080/v1/cdip/jobs/$PARENT_JOB_ID/decode-loop
```

Inspect the result:

```sh
curl -fsS -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  http://MANAGER_HOST:18080/v1/jobs/$PARENT_JOB_ID
```

## Observability

Use:

```sh
curl -fsS -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  http://MANAGER_HOST:18080/v1/observability

journalctl -u cmesh.service --no-pager -n 200
journalctl -u cmesh-worker.service --no-pager -n 200
journalctl -u cmesh-stage-daemon.service --no-pager -n 200
```

Keep the distributed plan, final job JSON, `/v1/observability`, package
`manifest.json`, package `checksums.txt`, and relevant journal snippets for
every production incident or validation run.

## Backup, Upgrade, And Uninstall

Manager state is local file state under `/var/lib/cmesh` by default. Follow
`docs/LINUX_BACKUP_RESTORE.md` before upgrades.

Uninstall workers:

```sh
sudo ./install-worker.sh stop
sudo ./install-worker.sh uninstall
```

Uninstall manager:

```sh
sudo ./install-manager-linux.sh stop
sudo ./install-manager-linux.sh uninstall
```

Uninstall keeps `/etc/cmesh` and `/var/lib/cmesh` by design. Remove those
directories only when intentionally destroying cluster identity and local state.

## Validated Evidence

Latest package-based beta evidence:

- Package: signed Linux production package under `dist/linux-production/<version>`
- Evidence root: `/tmp/cmesh-linux-beta-deployment-20260620135153`
- Installer E2E: `/tmp/cmesh-linux-beta-deployment-20260620135153/installers`
- Sliced E2E: `/tmp/cmesh-linux-beta-deployment-20260620135153/sliced`
- Result: installer E2E passed, sliced GGUF E2E passed, all created AWS
  instances were terminated.

## Not Production-Supported Yet

- Windows sliced execution
- macOS sliced execution
- GPU acceleration as a production guarantee
- arbitrary GGUF architectures
- public untrusted worker marketplace
- multi-manager consensus deployment
- automatic layer-shard publishing for every catalog model
