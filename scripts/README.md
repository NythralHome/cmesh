# Scripts

## Worker Install

macOS/Linux one-shot worker runner:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  CMESH_MANAGER_URL="https://cmesh.nythral.com" \
  CMESH_JOIN_TOKEN="replace-with-join-token" \
  sh
```

Linux worker as a systemd service:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.sh | \
  sudo env CMESH_MANAGER_URL="https://cmesh.nythral.com" \
  CMESH_JOIN_TOKEN="replace-with-join-token" \
  CMESH_INSTALL_SERVICE=true \
  sh
```

Windows PowerShell:

```powershell
$env:CMESH_MANAGER_URL="https://cmesh.nythral.com"
$env:CMESH_JOIN_TOKEN="replace-with-join-token"
iwr https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-worker.ps1 -UseB | iex
```

## Manager Install

Linux VPS with systemd:

```sh
curl -fsSL https://raw.githubusercontent.com/NythralHome/cmesh/main/scripts/install-manager-linux.sh | \
  sudo env DATABASE_URL="postgres://user:password@host:5432/cmesh_alpha?sslmode=require" \
  sh
```

If `CMESH_JOIN_TOKEN` is omitted, the manager installer generates one and stores it in `/etc/cmesh/manager.env`.
