# Alpha Deployment

This guide describes a trusted alpha deployment where one internet-accessible manager coordinates workers running on machines in different locations.

CMesh is not ready for an untrusted public marketplace yet. Use invite-only workers that you know.

## Topology

```text
VPS / public server
  cmesh manager

Remote machines
  cmesh worker run -> outbound HTTPS/HTTP connection to manager
```

Workers do not need inbound ports. They connect out to the manager.

## Build Binaries

```sh
make dist
```

Artifacts:

```text
dist/cmesh-darwin-arm64
dist/cmesh-darwin-amd64
dist/cmesh-linux-amd64
dist/cmesh-linux-arm64
dist/cmesh-windows-amd64.exe
```

## Guarded Alpha Deploy

For the hosted alpha, deploy through the release-asset guard:

```sh
make deploy-alpha VERSION=v0.1.0-alpha.44
```

The guard checks that the CLI binary and every worker desktop artifact referenced by the invite page are already downloadable from GitHub Releases. If any asset is still publishing, deployment stops before the manager is updated.

## Manager On A VPS

Generate an invite token:

```sh
openssl rand -hex 32
```

Run directly:

```sh
export CMESH_JOIN_TOKEN="replace-with-generated-token"
export CMESH_OPERATOR_TOKEN="replace-with-operator-token"
export CMESH_PUBLIC_URL="https://cmesh.example.com"
./cmesh-linux-amd64 manager start \
  --addr :8080 \
  --state-path /var/lib/cmesh/cmesh-state.json
```

Or install the manager as a Linux systemd service with an interactive wizard:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | sudo sh
```

The wizard asks for a domain and can install/configure Caddy for HTTPS automatically. Point DNS before enabling HTTPS:

```text
cmesh.example.com -> VPS_PUBLIC_IP
```

For cloud-init or automation, pass values non-interactively:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | \
  sudo env \
    CMESH_DOMAIN="cmesh.example.com" \
    CMESH_ADMIN_EMAIL="admin@example.com" \
    CMESH_INSTALL_CADDY=true \
    sh
```

If `CMESH_JOIN_TOKEN` is omitted, the installer generates one and stores it in `/etc/cmesh/manager.env`.

CMesh uses local file persistence by default. Manager restarts do not erase workers, jobs, and benchmark history:

```text
/var/lib/cmesh/cmesh-state.json
```

For larger deployments, Postgres is optional:

```sh
export CMESH_JOIN_TOKEN="replace-with-generated-token"
export CMESH_OPERATOR_TOKEN="replace-with-operator-token"
export CMESH_PUBLIC_URL="https://cmesh.example.com"
export DATABASE_URL="postgres://user:password@host:5432/cmesh_alpha?sslmode=require"
./cmesh-linux-amd64 manager start --addr :8080
```

CMesh runs the required schema migrations on startup when Postgres is used. `CMESH_OPERATOR_TOKEN` protects the cluster dashboard, read/admin APIs, and `/invite`, where the dashboard generates worker install commands.

Run with Docker Compose:

```sh
cd deployments/docker
export CMESH_JOIN_TOKEN="replace-with-generated-token"
export CMESH_OPERATOR_TOKEN="replace-with-operator-token"
export CMESH_PUBLIC_URL="https://cmesh.example.com"
docker compose up -d --build
```

Open firewall ports `80` and `443` when using Caddy. If you skip Caddy, expose `8080` or place CMesh behind your own reverse proxy.

## Manual Domain And HTTPS With Caddy

Point DNS:

```text
cmesh.example.com -> VPS_PUBLIC_IP
```

Caddyfile:

```text
cmesh.example.com {
  reverse_proxy 127.0.0.1:8080
}
```

Workers can then connect to:

```text
https://cmesh.example.com
```

## Connect A Remote Worker

For a guided first alpha with several real machines, use [FIRST_REAL_TEST.md](FIRST_REAL_TEST.md). Prefer the desktop worker app for donors; the install scripts below are the fallback path for terminal-only machines.

macOS/Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  CMESH_MANAGER_URL="https://cmesh.example.com" \
  CMESH_JOIN_TOKEN="replace-with-generated-token" \
  CMESH_CPU=4 \
  CMESH_MEMORY_GB=8 \
  CMESH_DISK_GB=50 \
  sh
```

Linux service with boot startup:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  sudo env CMESH_MANAGER_URL="https://cmesh.example.com" \
  CMESH_JOIN_TOKEN="replace-with-generated-token" \
  CMESH_INSTALL_SERVICE=true \
  CMESH_CPU=4 \
  CMESH_MEMORY_GB=8 \
  CMESH_DISK_GB=50 \
  sh
```

Worker service control:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sh -s -- status
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- stop
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- start
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | sudo sh -s -- uninstall
```

Manual macOS/Linux run:

```sh
chmod +x ./cmesh
./cmesh worker run \
  --manager https://cmesh.example.com \
  --token replace-with-generated-token \
  --name friend-mac \
  --cpu 4 \
  --memory-gb 8 \
  --disk-gb 50 \
  --benchmark
```

Windows PowerShell:

```powershell
$env:CMESH_MANAGER_URL="https://cmesh.example.com"
$env:CMESH_JOIN_TOKEN="replace-with-generated-token"
$env:CMESH_CPU="4"
$env:CMESH_MEMORY_GB="8"
$env:CMESH_DISK_GB="50"
iwr https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.ps1 -UseB | iex
```

Windows service control:

```powershell
$script = (iwr https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.ps1 -UseB).Content
iex "& { $script } -Action status"
iex "& { $script } -Action stop"
iex "& { $script } -Action start"
iex "& { $script } -Action uninstall"
```

Manual Windows PowerShell run:

```powershell
.\cmesh.exe worker run `
  --manager https://cmesh.example.com `
  --token replace-with-generated-token `
  --name friend-pc `
  --cpu 4 `
  --memory-gb 8 `
  --disk-gb 50 `
  --benchmark
```

## Submit A Test Job

```sh
./cmesh job submit \
  --manager https://cmesh.example.com \
  --type echo \
  --input "hello from the internet"
```

Inspect jobs:

```sh
./cmesh job list --manager https://cmesh.example.com
```

Dashboard:

```text
https://cmesh.example.com
```

Invite page:

```text
https://cmesh.example.com/invite
```

The dashboard prompts for `CMESH_OPERATOR_TOKEN` before showing cluster state or worker install commands.

## Current Alpha Limits

- Manager state is durable in the local state file by default. Postgres is optional for larger deployments.
- Join token protects worker registration. Operator token protects the dashboard and read/admin API for alpha deployments.
- Transport security depends on your reverse proxy. Use HTTPS for internet tests.
- Workers execute only supported CMesh job types. Current executor: `echo`.
- This is a trusted private alpha, not a public compute marketplace.
