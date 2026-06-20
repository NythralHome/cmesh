# CMesh Linux Security Hardening

This checklist is required before exposing a CMesh manager on a public VPS.

## Network

- Expose only SSH, HTTP, and HTTPS publicly.
- Prefer HTTPS through Caddy using `CMESH_DOMAIN` and `CMESH_INSTALL_CADDY=true`.
- Keep the manager service bound to a local or private upstream when Caddy is
  used.
- Do not expose worker-local stage daemon ports publicly.
- Do not expose worker-local control API ports publicly.

Example firewall intent:

```sh
ufw allow OpenSSH
ufw allow 80/tcp
ufw allow 443/tcp
ufw deny 18080/tcp
ufw deny 19781/tcp
ufw enable
```

If the manager intentionally binds `0.0.0.0:18080` for a private VPC proof,
restrict it with cloud security groups to known worker/admin IPs.

## Secrets

- `CMESH_JOIN_TOKEN` is for workers only.
- `CMESH_OPERATOR_TOKEN` is for admin/operator APIs only.
- Never reuse the join token as the operator token.
- Store manager secrets in `/etc/cmesh/manager.env` with mode `0600`.
- Store worker secrets in `/etc/cmesh/worker.env` with mode `0600`.
- Rotate tokens after demos, leaked shell history, or shared screenshots.
- Do not publish tokens in GitHub releases, docs, logs, or screenshots.

## Systemd

Manager and worker services must run as `cmesh:cmesh` and include:

- `NoNewPrivileges=true`
- `PrivateTmp=true`
- `ProtectSystem=strict`
- `ProtectHome=true`
- `ReadWritePaths=/var/lib/cmesh`

## API Access

- `/health` may be public.
- Operator APIs must require `Authorization: Bearer $CMESH_OPERATOR_TOKEN`.
- Worker join must require `CMESH_JOIN_TOKEN`.
- Worker node auth tokens must not appear in `/v1/nodes`.
- One worker node token must not be able to heartbeat, poll, or leave as another
  worker.

## Runtime And Models

- Runtime artifacts must be checksum-verified.
- Model artifacts in the Linux production matrix must be checksum-verified.
- Stage daemon sessions should be cleaned up after canceled jobs.
- Keep `/var/lib/cmesh` on a disk with enough free space for model shards and
  runtime cache.

## Validation

Run:

```sh
scripts/production-security-smoke.sh
scripts/linux-production-manager-install-smoke.sh
scripts/linux-production-worker-install-smoke.sh
```

The production security smoke validates:

- manager refuses unsafe public startup without required tokens
- admin endpoints reject missing/wrong/join-token auth
- worker join rejects missing/wrong/operator-token auth
- per-node worker auth isolation
- local worker control API token rejection

## Incident Cleanup

If a public token leaks:

1. Stop manager and workers.
2. Generate new join/operator tokens.
3. Update `/etc/cmesh/manager.env`.
4. Reinstall or update workers with the new join token.
5. Restart services.
6. Verify `/v1/cluster` and `/v1/observability`.
