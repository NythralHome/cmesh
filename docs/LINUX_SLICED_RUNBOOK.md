# CMesh Linux Sliced-Model Runbook

This is the operator runbook for the Linux production sliced path. It uses
release package artifacts, not local development paths.

## Scope

- Platform: `linux/amd64`
- Runtime: `llama.cpp-b9704-linux-amd64-rpc-stage`
- Supported model: `qwen2.5-14b-instruct-q4-k-m`
- Required topology: one manager plus three stage workers
- Minimum worker allocation: `6 GB RAM`, `20 GB disk`
- Recommended worker count: `3`
- Validated placement policy: `memory_disk_weighted_layers`

See `docs/LINUX_MODEL_MATRIX.md` for exact model file, SHA256, and evidence.

## Release Package

Download or mirror the Linux production package, then verify it:

```sh
cd cmesh-linux-production
shasum -a 256 -c checksums.txt
shasum -a 256 -c llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz.sha256
```

The package must contain:

- `cmesh-linux-amd64`
- `install-manager-linux.sh`
- `install-worker.sh`
- `llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz`
- `llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz.sha256`
- `docs/LINUX_MODEL_MATRIX.md`
- `docs/PRODUCTION_INSTALL.md`

## Manager Install

Run on the manager host:

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

## Worker Install

Run on each worker host:

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
journalctl -u cmesh-worker.service --no-pager -n 80
journalctl -u cmesh-stage-daemon.service --no-pager -n 80
```

Verify from the manager:

```sh
curl -fsS -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  http://MANAGER_HOST:18080/v1/observability
```

## Model Artifact

Use the production-supported model:

```sh
MODEL_ID=qwen2.5-14b-instruct-q4-k-m
MODEL_FILE=Qwen2.5-14B-Instruct-Q4_K_M.gguf
MODEL_URL=https://huggingface.co/bartowski/Qwen2.5-14B-Instruct-GGUF/resolve/main/Qwen2.5-14B-Instruct-Q4_K_M.gguf
MODEL_SHA256=d989c91de35f32c18bdb8bec96a4b9fff2c3e5bca066503c63a5ca54dd537a4b
```

Download once, verify, then distribute or mount it where worker install/run
commands expect it:

```sh
curl -fL "$MODEL_URL" -o "$MODEL_FILE"
echo "$MODEL_SHA256  $MODEL_FILE" | shasum -a 256 -c -
```

## Readiness And Placement

Check cluster readiness:

```sh
curl -fsS -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  http://MANAGER_HOST:18080/v1/observability
```

Check sliced placement:

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
- every stage within worker memory and disk limits

## Distributed Generate

Create a distributed generation job:

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
```

Prepare resident stage sessions:

```sh
PARENT_JOB_ID=$(jq -r '.job.id' distributed-generate.json)
curl -fsS -X POST \
  -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{}' \
  http://MANAGER_HOST:18080/v1/cdip/jobs/$PARENT_JOB_ID/prepare
```

Run the decode loop:

```sh
curl -fsS -X POST \
  -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{}' \
  http://MANAGER_HOST:18080/v1/cdip/jobs/$PARENT_JOB_ID/decode-loop
```

Inspect the final parent job:

```sh
curl -fsS -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  http://MANAGER_HOST:18080/v1/jobs/$PARENT_JOB_ID
```

## Recovery

If a stage worker fails:

1. Check `/v1/observability` for failed stage job and stage daemon status.
2. Restart the worker services:

   ```sh
   sudo systemctl restart cmesh-stage-daemon.service
   sudo systemctl restart cmesh-worker.service
   ```

3. Re-run `distributed-plan`.
4. If the parent job cannot recover, create a new distributed-generate job.

## Cleanup

On workers:

```sh
sudo ./install-worker.sh stop
sudo ./install-worker.sh uninstall
```

On manager:

```sh
sudo ./install-manager-linux.sh stop
sudo ./install-manager-linux.sh uninstall
```

Uninstall keeps `/etc/cmesh` and `/var/lib/cmesh` by design. Remove them only
when intentionally destroying cluster identity and local state.

## Evidence To Keep

For every production sliced run, keep:

- `distributed-plan` response
- `distributed-generate` response
- parent job final JSON
- `/v1/observability` output
- `journalctl` snippets for manager, workers, and stage daemons
- package `manifest.json`
- package `checksums.txt`
