# SCC Backend VPS Stack

This is the source-of-truth stack note for the backend repo.

## Decision

SCC now uses the VPS for all backend-side runtime infrastructure:

| Concern | Current target |
|---|---|
| Frontend hosting | Vercel |
| Backend API | Docker container on VPS, pulled from GHCR or loaded locally |
| Reverse proxy / HTTPS | Caddy container on VPS |
| Database | PostgreSQL container on VPS |
| Object/image storage | MinIO container on VPS |
| Upload method | Backend returns MinIO presigned PUT URLs |
| Temporary public hostnames | `api.<vps-ip>.sslip.io`, `storage.<vps-ip>.sslip.io` |

NeonDB and Cloudflare R2 are intentionally not used for the current backend runtime. nginx/certbot are also not part of the target path; Caddy handles HTTPS.

## VPS container names

`scripts/deploy-vps.sh` manages these containers:

| Container | Image | Purpose |
|---|---|---|
| `scc-caddy` | `caddy@sha256:<digest>` | Public HTTPS reverse proxy |
| `scc-backend` | `ghcr.io/<owner>/scc-backend@sha256:<digest>` or loaded local image | Go API |
| `scc-postgres` | `postgres@sha256:<digest>` | PostgreSQL data store |
| `scc-minio` | `minio/minio@sha256:<digest>` | S3-compatible object store |

Shared Docker network: `scc-net`

Named volumes:

- `scc-caddy-data`
- `scc-caddy-config`
- `scc-postgres-data`
- `scc-minio-data`

## Required public hosts

If there is no purchased domain, use `sslip.io` hostnames from the VPS public IP:

- `api.103.117.151.158.sslip.io` -> Caddy -> `scc-backend:8080`
- `storage.103.117.151.158.sslip.io` -> Caddy -> `scc-minio:9000`

When a real domain exists, change only `/opt/scc-backend/.env`:

```env
API_HOST=api.<domain>
STORAGE_HOST=storage.<domain>
MINIO_INTERNAL_ENDPOINT=scc-minio:9000
MINIO_INTERNAL_USE_SSL=false
MINIO_PUBLIC_ENDPOINT=storage.<domain>
MINIO_PUBLIC_USE_SSL=true
```

Then rerun `scripts/deploy-vps.sh`.

The MinIO bucket is private (`mc anonymous set none`). The API uses the Docker-network endpoint for bucket/stat/magic-byte verification and the public Caddy hostname only for short-lived signed browser PUT/GET URLs. Production CORS must list explicit HTTPS frontend origins; wildcard origins are rejected.

The production API does not provision the bucket or mutate its anonymous
policy at startup. `deploy-vps.sh` performs those administrative operations
with the root pair; the API starts with a readiness-only check so its runtime
credential can be a prefix-scoped MinIO service account. Follow
[`MINIO_SERVICE_ACCOUNT_ROTATION.md`](MINIO_SERVICE_ACCOUNT_ROTATION.md) to
separate `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` from the root pair with an
atomic, validated, reversible rotation.

### MinIO CORS compatibility and restart contract

Some current MinIO/mc pairs, including the inspected `RELEASE.2025-09-07` server with the `RELEASE.2025-08-13` client, return `NotImplemented` for `mc cors set` even though the supported global API setting is available. The deploy handles this deterministically:

1. It first attempts the bucket-scoped GET/PUT/HEAD CORS policy.
2. If that operation is unsupported, it reads `mc admin config get scc api` and sets `cors_allow_origin` to the exact validated `CORS_ORIGINS` value when it differs.
3. A changed global value causes exactly one host-controlled `docker restart scc-minio`; the deploy waits for MinIO readiness and then reads the global value back through the pinned mc image.
4. An already exact global value causes no restart. Bucket CORS success also causes no restart.

`MINIO_CONFIGURE_CORS=required` fails the release only when both bucket CORS and the global fallback fail. `best-effort` records `unconfigured-best-effort` in release metadata and continues if both mechanisms fail. The private-bucket command remains mandatory in every mode. A global fallback restart happens before the backend candidate is started, after the predeploy backup and migration contract have run.

After deploy, run the read-only public verification with the real hosts:

```bash
DOCTOR_ENABLE_PUBLIC_CHECKS=true \
PUBLIC_API_HEALTHCHECK_URL=https://api.<domain>/api/v1/readyz \
PUBLIC_STORAGE_HEALTHCHECK_URL=https://storage.<domain>/minio/health/live \
scripts/doctor-vps.sh
```

The doctor sends OPTIONS preflights to a non-existent object key. It requires each configured origin to be echoed exactly and requires an unlisted audit origin to receive no `Access-Control-Allow-Origin`; this catches `*` and origin-list drift without reading or writing an object. Set `DOCTOR_VERIFY_MINIO_CORS=false` only for a diagnosed probe incompatibility, and record that exception.

## Migration policy

The PostgreSQL container is private on the VPS, so the deploy job invokes the target image's `/app/scc-migrate` as a one-shot container on `scc-net`; it never exposes PostgreSQL publicly.

Current production policy:

```env
ENV=production
AUTO_MIGRATE=false
SEED_DATA=false
```

The deploy script rejects any other values before Docker work. After the required backup it runs the immutable target image's `scc-migrate validate`, `check`, `status`, `up`, `status`, and `version` commands in that order. Production API startup rejects `AUTO_MIGRATE=true` and rejects a missing, dirty, checksum-mismatched or pending migration ledger.

## Release safety

- CI deploys the immutable digest produced by the same build job, not the mutable `latest` tag.
- PostgreSQL, MinIO, the MinIO client, and Caddy are configured as repository digest references. A deploy refuses mutable infrastructure tags and refuses to reuse an existing PostgreSQL or MinIO container whose resolved image ID differs from the configured digest.
- `flock` serializes VPS deployments.
- Every release stores secret-free metadata, protected migration command artifacts/version evidence, and a required predeploy PostgreSQL dump validated by `pg_restore --list` under `/opt/scc-backend/deploy/`.
- The mode-`600` application env is parsed without evaluation and normalized into short-lived, least-privilege Docker env files, so shell quotes are not accidentally passed as credential bytes and the API container does not inherit PostgreSQL/MinIO root credentials or image controls. CI stages its read-only GHCR token and deploy script in an unpredictable mode-`700` remote directory rather than fixed `/tmp` filenames.
- A candidate runs with `RUN_BACKGROUND_JOBS=false` and must pass health on a loopback-only port before the current backend is stopped. The activated backend runs with `RUN_BACKGROUND_JOBS=true` as the single cron owner; the API binary must honor this runtime flag.
- The previous backend and Caddy containers remain available until internal and public health gates pass; post-switch failure restores them automatically.
- `/opt/scc-backend/Caddyfile` is installed atomically, mounted read-only, and verified with an explicit Caddy restart.
- Public API and storage health are release gates. The Vercel login page is an optional gate configured by the `FRONTEND_HEALTHCHECK_URL` repository variable.

### Docker-native healthchecks

Every long-running container created by `deploy-vps.sh` carries a versioned
`io.smartcover.healthcheck` label and a Docker-native healthcheck:

| Container | In-container check | Secret handling |
|---|---|---|
| `scc-backend` | Alpine BusyBox `wget` to `http://127.0.0.1:${PORT}/api/v1/readyz` | No auth value; `PORT` is an explicit non-secret runtime override |
| `scc-postgres` | `pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"` | Uses only environment variable references; no password appears in argv |
| `scc-minio` | image-provided `curl` to `/minio/health/ready` on loopback | MinIO's health endpoint requires no root or application credential |
| `scc-caddy` | image-provided `curl` to the loopback admin `/config/` endpoint | Reads no application secret and discards the response body |

These commands were checked against the pinned production image set. The
doctor verifies both Docker's runtime status and the exact managed
command/revision contract; a missing or drifted healthcheck is a failure rather
than an ignorable warning.

Backend and Caddy are replaced on each release, so their healthchecks are
installed during the normal candidate/switch flow. Existing PostgreSQL and
MinIO containers cannot gain a healthcheck through `docker update`. After the
required PostgreSQL predeploy dump, the deploy therefore performs a one-time
controlled recreation when either healthcheck is missing or drifted: it stops
and renames the old container, starts the same immutable image with the same
validated named volume/network/env, waits for readiness, and only then removes
the old container. A failed replacement is removed and the original container
is renamed back and restarted. Release metadata records `unchanged`,
`reconciled`, `reconciled-previous-retained`, `failed-original-restored`, or
`rollback-failed` independently for PostgreSQL and MinIO. Expect a short
dependency interruption during the first reconciliation.

Docker marks a container unhealthy but does not restart it merely because a
healthcheck failed. Keep `doctor-vps.sh`/external monitoring and alerting in
place; `--restart unless-stopped` handles process/container exits, not an
unhealthy state by itself.

### Host swap safety net

The 2 GiB VPS should keep a small disk-backed safety net for short memory
spikes. This is a one-time reviewed host operation and is deliberately not run
inside every application release:

```bash
sudo -n install -m 0755 scripts/provision-vps-swap.sh \
  /opt/scc-backend/bin/provision-vps-swap.sh
sudo -n /opt/scc-backend/bin/provision-vps-swap.sh
sudo -n install -m 0644 deploy/99-scc-swap.conf \
  /etc/sysctl.d/99-scc-swap.conf
sudo -n sysctl -p /etc/sysctl.d/99-scc-swap.conf
```

The provisioner defaults to a 1 GiB `/swapfile` and retains at least 256 MiB
of free disk beyond the requested allocation. It is root-only, lock-protected,
idempotent, refuses symlinks/non-swap files, keeps mode `0600`, verifies
activation, and installs one canonical `/etc/fstab` entry atomically. Existing
valid swapfiles are never reformatted or resized in place. Override
`SWAP_SIZE_MIB` only during a reviewed capacity change. The versioned sysctl
policy keeps `vm.swappiness=10` so swap remains an emergency buffer rather than
the normal working set.

Container rollback does not roll back database schema. Use backward-compatible expand/migrate/contract changes. The predeploy dump and migration ledger/output are retained for reviewed recovery, but data is never restored automatically because either application version may have accepted newer writes. If a post-migration stage fails, inspect the recorded before/after/verified versions and ledger; roll the container back only when the forward schema remains compatible.

## Security notes

- Do not commit `/opt/scc-backend/.env` or real secrets.
- Keep `POSTGRES_PASSWORD` plain for the PostgreSQL container, but URL-encode it inside `DATABASE_URL` when it contains URL-reserved characters.
- Keep public API/storage behind Caddy HTTPS.
- MinIO CORS is best-effort during deploy by default (`MINIO_CONFIGURE_CORS=best-effort`). Unsupported bucket CORS falls back to exact global API CORS; only failure of both mechanisms is non-fatal in this mode.
- Set `CORS_ORIGINS` to the exact Vercel origin(s); wildcard CORS is not a production setting.
- Rotate `JWT_SECRET` and `POSTGRES_PASSWORD` before real production use. First
  move API/backup access to the dedicated MinIO service account using
  `docs/MINIO_SERVICE_ACCOUNT_ROTATION.md`; rotate the MinIO root pair in a
  separate reviewed maintenance window afterward.
- Resolve infrastructure tags to reviewed multi-platform digests during a planned upgrade, record those digest references in `/opt/scc-backend/.env`, and test a scratch restore before changing the PostgreSQL or MinIO digest.
- Add scheduled encrypted offsite PostgreSQL and MinIO backups plus restore drills before relying on VPS-local state. Per-release PostgreSQL dumps are local checkpoints, not the backup system.
