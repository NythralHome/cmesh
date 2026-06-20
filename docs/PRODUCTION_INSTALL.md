# CMesh Production Install Runbook

This runbook is the production path for installing a private CMesh cluster.
It assumes Linux hosts with systemd. Desktop workers can still use the packaged
Worker app, but the production sliced-model path is validated first on Linux
services.

## Roles

- Manager: owns cluster state, scheduling, operator API, join API, and CDIP
  activation relay.
- Worker: joins the manager, advertises resources, executes jobs, and can run a
  resident stage daemon for sliced model execution.
- Stage daemon: local worker-side service that keeps model stage sessions and KV
  state resident across token steps.

## Manager Install

Run as root on the manager host:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | \
  sudo CMESH_NONINTERACTIVE=true \
    CMESH_DOMAIN=cluster.example.com \
    CMESH_INSTALL_CADDY=true \
    sh
```

For a private/VPC-only manager, skip Caddy and bind the API directly:

```sh
sudo CMESH_NONINTERACTIVE=true \
  CMESH_ADDR=0.0.0.0:18080 \
  CMESH_PUBLIC_URL=http://manager-private-ip:18080 \
  ./scripts/install-manager-linux.sh
```

The installer creates:

- `/usr/local/bin/cmesh`
- `/etc/systemd/system/cmesh.service`
- `/etc/cmesh/manager.env` with mode `0600`
- `/var/lib/cmesh` for local state

Secrets are generated when not provided:

- `CMESH_JOIN_TOKEN`
- `CMESH_OPERATOR_TOKEN`

They are stored in `/etc/cmesh/manager.env` and are not printed unless
`CMESH_PRINT_SECRETS=true`.

## Worker Install

Run as root on each worker host:

```sh
sudo CMESH_NONINTERACTIVE=true \
  CMESH_MANAGER_URL=https://cluster.example.com \
  CMESH_JOIN_TOKEN=replace-with-manager-join-token \
  CMESH_INSTALL_SERVICE=true \
  CMESH_CPU=2 \
  CMESH_MEMORY_GB=6 \
  CMESH_DISK_GB=40 \
  ./scripts/install-worker.sh
```

For production sliced model workers, keep the stage daemon enabled. On Linux
service installs it is enabled automatically when the pinned llama.cpp stage
runtime is available:

```sh
sudo CMESH_NONINTERACTIVE=true \
  CMESH_MANAGER_URL=http://manager-private-ip:18080 \
  CMESH_JOIN_TOKEN=replace-with-manager-join-token \
  CMESH_INSTALL_SERVICE=true \
  CMESH_STAGE_DAEMON=true \
  CMESH_STAGE_DAEMON_BACKEND=llama.cpp-resident \
  CMESH_CPU=2 \
  CMESH_MEMORY_GB=6 \
  CMESH_DISK_GB=40 \
  ./scripts/install-worker.sh
```

The worker installer creates:

- `/usr/local/bin/cmesh`
- `/etc/systemd/system/cmesh-worker.service`
- `/etc/systemd/system/cmesh-stage-daemon.service` when stage daemon is enabled
- `/etc/cmesh/worker.env` with mode `0600`
- `/var/lib/cmesh/cache` for runtime artifacts and model shards
- `/var/lib/cmesh/stage-sessions` for resident stage sessions

## Lifecycle

Manager:

```sh
sudo ./scripts/install-manager-linux.sh status
sudo ./scripts/install-manager-linux.sh restart
sudo ./scripts/install-manager-linux.sh uninstall
```

Worker:

```sh
sudo ./scripts/install-worker.sh status
sudo ./scripts/install-worker.sh restart
sudo ./scripts/install-worker.sh uninstall
```

Uninstall removes systemd units. Manager state and token files are intentionally
left in `/var/lib/cmesh` and `/etc/cmesh` so accidental uninstall does not erase
cluster identity.

## Verification

Local installer checks:

```sh
scripts/installers-dry-run-smoke.sh
```

Full local production gate without AWS:

```sh
CMESH_RUN_AWS_E2E=false scripts/production-readiness-gate.sh
```

Cautious AWS installer proof:

```sh
CMESH_AWS_INSTANCE_COUNT=3 \
CMESH_AWS_INSTANCE_TYPE=t3.small \
CMESH_AWS_VOLUME_SIZE=20 \
scripts/aws-installers-e2e.sh
```

The AWS script tags resources, records instance IDs in its state directory, and
terminates instances plus deletes the security group unless
`CMESH_KEEP_AWS_RESOURCES=true`.

## Production Checks

Before calling a build production-ready, verify:

- manager health endpoint responds
- operator endpoints require `CMESH_OPERATOR_TOKEN`
- workers join with `CMESH_JOIN_TOKEN`
- `/v1/cluster` reports expected online workers
- `cmesh-worker.service` is active on every worker
- `cmesh-stage-daemon.service` is active on sliced-model workers
- runtime artifact verification passes
- memory-aware sliced placement is feasible and executable
- AWS E2E cleanup state shows all created instances terminated
