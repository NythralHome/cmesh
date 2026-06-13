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

## Manager On A VPS

Generate an invite token:

```sh
openssl rand -hex 32
```

Run directly:

```sh
export CMESH_JOIN_TOKEN="replace-with-generated-token"
./cmesh-linux-amd64 manager start --addr :8080
```

Run with Docker Compose:

```sh
cd deployments/docker
export CMESH_JOIN_TOKEN="replace-with-generated-token"
docker compose up -d --build
```

Open firewall port `8080`, or put CMesh behind HTTPS.

## Domain And HTTPS With Caddy

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

macOS/Linux:

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

## Current Alpha Limits

- Manager state is in-memory. Restarting manager clears nodes, jobs, and benchmark history.
- Join token protects worker registration, but there is no user auth for dashboard/API yet.
- Transport security depends on your reverse proxy. Use HTTPS for internet tests.
- Workers execute only supported CMesh job types. Current executor: `echo`.
- This is a trusted private alpha, not a public compute marketplace.

