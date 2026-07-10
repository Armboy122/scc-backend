# SCC Backup and Restore

This pack creates local, transactionally published backups for the VPS PostgreSQL database and the current contents of the MinIO bucket. Local backup is the first recovery layer; it is **not offsite disaster recovery**.

## Safety model

- `/opt/scc-backend/.env` must be a regular non-symlink file with no group/other permissions (normally mode `600`). The backup script parses its allowlisted values without evaluating shell syntax, printing values, or exporting them to child processes. Scratch restore does not need or read live application secrets.
- Backup and restore use separate non-blocking `flock` files. Concurrent runs fail before Docker work starts.
- A backup is written below the backup root as a hidden `.partial` directory. It becomes visible as a completed timestamp directory only after DB validation, MinIO mirror, inventory, metadata, manifest, and complete marker are written.
- Restore defaults to isolated scratch containers, volumes, and a private scratch network with no published host ports.
- Live overwrite is not implemented. `--target live` is refused without the exact destructive confirmation and still exits after confirmation, directing the operator to scratch restore and review.

## Dependencies

- Bash
- Docker access for the service user
- Python 3
- `flock` from `util-linux`
- Existing `scc-postgres`, `scc-minio`, and `scc-net` for backup. Full verification uses the immutable `POSTGRES_IMAGE` digest supplied by the operator and does not require the live database container.
- The protected application env must contain the dedicated MinIO service
  account described in `MINIO_SERVICE_ACCOUNT_ROTATION.md`. The backup mirror
  needs only bucket list/location and `evidence/v1/*` object read access; it
  must not receive MinIO root/admin credentials.
- Enough local disk for a PostgreSQL dump plus a full mirror of current MinIO objects and temporary staging space

## Backup artifact

Completed backups use UTC IDs such as:

```text
/opt/scc-backend/backups/20260710T021500Z/
  .complete
  SHA256SUMS.jsonl
  metadata.env
  object-inventory.jsonl
  postgres.dump
  objects/
```

- `postgres.dump` is a custom-format logical dump and must pass `pg_restore --list` before publication.
- `objects/` is an authenticated `mc mirror` of the configured bucket's current objects.
- `object-inventory.jsonl` records every mirrored object path and size.
- `SHA256SUMS.jsonl` records the SHA-256 and size of the DB dump, metadata, inventory, and every object.
- `.complete` records the SHA-256 of the manifest. Its presence is the publication boundary.
- `metadata.env` contains operational names, counts, timestamps, and `storage_scope=local-only`; it contains no credentials.

These hashes detect accidental corruption and incomplete copies. They are not signed and cannot prove authenticity after a malicious VPS compromise; the offsite layer should add authenticated encryption or a separately protected signature.

The MinIO mirror captures current object bytes. It does not back up MinIO users, credentials, bucket policies, CORS configuration, lifecycle rules, or old object versions. PostgreSQL `pg_dump` does not include cluster roles or other global objects.

PostgreSQL and MinIO are captured sequentially, not as one distributed point-in-time transaction. Workflows that write a DB record and object concurrently can produce a small cross-system timing gap. Quiesce writes for a strict coordinated recovery point.

## Manual backup

Install scripts in a path owned by root and executable by `sccvps`, for example `/opt/scc-backend/bin/`, then run:

```bash
APP_ENV_PATH=/opt/scc-backend/.env \
BACKUP_ROOT=/opt/scc-backend/backups \
/opt/scc-backend/bin/backup-vps.sh
```

Default local retention is a deterministic union of:

- newest backup from each of 7 UTC days;
- newest backup from each of 4 ISO weeks;
- newest backup from each of 6 UTC calendar months.

The newest completed backup is always retained. Incomplete directories do not participate in retention and are removed by the failed run's cleanup trap.

## Verification and freshness

Full verification checks the complete-marker hash, every file checksum and size, safe relative paths, exact object inventory, absence of unexpected artifacts, metadata counts, and `pg_restore --list`:

```bash
POSTGRES_IMAGE='postgres@sha256:<reviewed-digest>' \
/opt/scc-backend/bin/verify-backup.sh \
  --backup /opt/scc-backend/backups/20260710T021500Z
```

Verify the latest backup and require it to be no older than 26 hours:

```bash
BACKUP_ROOT=/opt/scc-backend/backups \
/opt/scc-backend/bin/verify-backup.sh \
  --latest \
  --max-age-seconds 93600
```

`--freshness-only` checks required artifact presence, backup age, and the manifest file's hash from `.complete`; it intentionally skips hashing all payload files and `pg_restore --list`. Use full verification for integrity, especially before restore.

## Scratch restore drill

Run a verified restore into isolated resources:

```bash
POSTGRES_IMAGE='postgres@sha256:<reviewed-digest>' \
MINIO_IMAGE='minio/minio@sha256:<reviewed-digest>' \
MINIO_MC_IMAGE='minio/mc@sha256:<reviewed-digest>' \
/opt/scc-backend/bin/restore-vps.sh \
  --backup /opt/scc-backend/backups/20260710T021500Z \
  --target scratch
```

The script first runs full backup verification. It then creates timestamped resources similar to:

```text
scc-restore-net-20260710T030000Z
scc-restore-postgres-20260710T030000Z
scc-restore-minio-20260710T030000Z
```

It restores the logical DB, runs `SELECT 1`, mirrors objects into a scratch bucket, and compares restored object count with backup metadata. Scratch resources remain for inspection by default.

The three restore image variables are required digest references. Use the same reviewed image set recorded for the deployment that produced the backup unless a tested compatibility plan says otherwise; restore never falls back to mutable tags.

After inspection, remove only the exact scratch resources reported by the restore:

```bash
docker rm -f scc-restore-postgres-20260710T030000Z
docker rm -f scc-restore-minio-20260710T030000Z
docker volume rm scc-restore-postgres-20260710T030000Z
docker volume rm scc-restore-minio-20260710T030000Z
docker network rm scc-restore-net-20260710T030000Z
```

Set `KEEP_SCRATCH=false` to remove them automatically after successful verification. Failed scratch restores clean up their partial containers, volumes, network, and temporary credential file.

Promotion into live service is intentionally a separate, reviewed runbook. It must define a maintenance window, stop application writes, capture a final backup, reconcile schema versions, restore both systems, verify counts and application workflows, and preserve a rollback point.

## systemd installation

Review the service user and paths, then install the unit files from `deploy/systemd/`:

```bash
sudo install -d -m 0755 -o root -g root /opt/scc-backend/bin
sudo install -d -m 0700 -o sccvps -g sccvps /opt/scc-backend/backups /opt/scc-backend/restore
sudo install -m 0755 scripts/backup-vps.sh /opt/scc-backend/bin/backup-vps.sh
sudo install -m 0755 scripts/verify-backup.sh /opt/scc-backend/bin/verify-backup.sh
sudo install -m 0755 scripts/restore-vps.sh /opt/scc-backend/bin/restore-vps.sh
sudo install -m 0644 deploy/systemd/scc-backup.service /etc/systemd/system/scc-backup.service
sudo install -m 0644 deploy/systemd/scc-backup.timer /etc/systemd/system/scc-backup.timer
sudo install -m 0644 deploy/systemd/scc-backup-freshness.service /etc/systemd/system/scc-backup-freshness.service
sudo install -m 0644 deploy/systemd/scc-backup-freshness.timer /etc/systemd/system/scc-backup-freshness.timer
sudo systemctl daemon-reload
sudo systemctl enable --now scc-backup.timer
sudo systemctl enable --now scc-backup-freshness.timer
```

The backup runs daily at 02:15 UTC with up to 30 minutes randomized delay. The freshness gate runs four times daily with up to 15 minutes randomized delay. Both timers use `Persistent=true`, so a missed calendar run is triggered after the VPS returns.

Inspect without revealing environment values:

```bash
systemctl list-timers 'scc-backup*'
systemctl status scc-backup.service
journalctl -u scc-backup.service --since today
```

Connect service failures to an external alert before relying on the schedule; systemd journal status alone is not notification.

## Optional offsite encryption hook

`OFFSITE_HOOK` is an optional absolute executable path. After the local directory is complete, the backup script calls:

```text
OFFSITE_HOOK /absolute/path/to/completed-backup
```

The hook must independently:

1. encrypt the snapshot before it leaves the VPS;
2. upload to versioned offsite storage under a non-VPS credential;
3. verify the remote checksum;
4. avoid logging encryption keys or storage credentials;
5. return non-zero on any incomplete upload.

Keep hook credentials in a separate mode-`600` file, not the application `.env`, and treat the completed local directory as read-only input so its manifest remains valid. Set `OFFSITE_REQUIRED=true` when a missing or failed offsite copy should fail the systemd backup job; this setting also requires a configured hook. The completed local backup remains available after a hook failure, but it is still only a local `.complete`: `.offsite-failed` is written and the job exits non-zero. Only a successful hook creates `.offsite-complete`. No offsite provider or encryption implementation is bundled, so the base pack must not be represented as offsite-capable until a reviewed hook is installed and a remote restore drill succeeds.
