# CMesh Linux Backup, Restore, And Upgrade

The Linux production manager uses a local JSON state file by default. The
default path is `/var/lib/cmesh/cmesh-state.json`, configured through
`CMESH_STATE_PATH` in `/etc/cmesh/manager.env`.

## Back Up Manager State

Stop the manager or take the backup during a quiet window:

```sh
sudo systemctl stop cmesh.service
sudo install -d -m 0700 /var/backups/cmesh
sudo cp /var/lib/cmesh/cmesh-state.json /var/backups/cmesh/cmesh-state-$(date -u +%Y%m%d%H%M%S).json
sudo cp /etc/cmesh/manager.env /var/backups/cmesh/manager-env-$(date -u +%Y%m%d%H%M%S)
sudo systemctl start cmesh.service
```

Keep both files secure. `manager.env` contains `CMESH_JOIN_TOKEN` and
`CMESH_OPERATOR_TOKEN`.

## Restore Manager State

```sh
sudo systemctl stop cmesh.service
sudo cp /var/backups/cmesh/cmesh-state-YYYYMMDDHHMMSS.json /var/lib/cmesh/cmesh-state.json
sudo chown cmesh:cmesh /var/lib/cmesh/cmesh-state.json
sudo chmod 0600 /var/lib/cmesh/cmesh-state.json
sudo systemctl start cmesh.service
```

Verify:

```sh
curl -fsS http://MANAGER_HOST:18080/health
curl -fsS -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  http://MANAGER_HOST:18080/v1/cluster
curl -fsS -H "Authorization: Bearer $CMESH_OPERATOR_TOKEN" \
  http://MANAGER_HOST:18080/v1/observability
```

## Upgrade

1. Download the new Linux production package.
2. Verify `checksums.txt`.
3. Back up manager state and `/etc/cmesh/manager.env`.
4. Replace `/usr/local/bin/cmesh` with the new `cmesh-linux-amd64`.
5. Restart the manager:

   ```sh
   sudo systemctl restart cmesh.service
   ```

6. Upgrade workers one at a time:

   ```sh
   sudo systemctl stop cmesh-worker.service
   sudo install -m 0755 cmesh-linux-amd64 /usr/local/bin/cmesh
   sudo systemctl restart cmesh-stage-daemon.service
   sudo systemctl restart cmesh-worker.service
   ```

7. Verify `/v1/observability`.

## Rollback

1. Stop manager and workers.
2. Restore the previous `/usr/local/bin/cmesh`.
3. Restore manager state backup only if the new version changed state in a way
   the old version cannot read.
4. Start manager, then workers.
5. Verify `/v1/cluster` and `/v1/observability`.

## Validation

Run:

```sh
scripts/linux-manager-backup-restore-smoke.sh
```
