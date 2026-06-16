# Scripts

## Worker Install

macOS/Linux one-shot worker runner:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  CMESH_MANAGER_URL="https://cmesh.nythral.com" \
  CMESH_JOIN_TOKEN="replace-with-join-token" \
  CMESH_CPU=4 \
  CMESH_MEMORY_GB=8 \
  CMESH_DISK_GB=50 \
  sh
```

Linux worker as a systemd service:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  sudo env CMESH_MANAGER_URL="https://cmesh.nythral.com" \
  CMESH_JOIN_TOKEN="replace-with-join-token" \
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

Windows PowerShell:

```powershell
$env:CMESH_MANAGER_URL="https://cmesh.nythral.com"
$env:CMESH_JOIN_TOKEN="replace-with-join-token"
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

## Manager Install

Linux VPS with systemd:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | sudo sh
```

Non-interactive VPS install with Caddy HTTPS:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | \
  sudo env \
    CMESH_DOMAIN="cmesh.example.com" \
    CMESH_ADMIN_EMAIL="admin@example.com" \
    CMESH_INSTALL_CADDY=true \
    sh
```

If `CMESH_JOIN_TOKEN` is omitted, the manager installer generates one and stores it in `/etc/cmesh/manager.env`.

## Alpha Deploy Guard

Use the guarded alpha deploy script after pushing a release tag:

```sh
CMESH_VERSION=v0.1.0-alpha.44 scripts/deploy-alpha.sh
```

The script checks every release asset used by the manager invite page before touching the VPS. It refuses to deploy while GitHub is still publishing desktop installers, which prevents broken download links such as a missing macOS DMG.

To check release readiness without deploying:

```sh
CMESH_VERSION=v0.1.0-alpha.44 CMESH_DRY_RUN=true scripts/deploy-alpha.sh
```
