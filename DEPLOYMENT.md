# VPS Backend Deployment

SCC backend production target: Ubuntu VPS running Docker containers for the API, PostgreSQL, MinIO, and Caddy. Caddy owns public HTTP/HTTPS and obtains certificates automatically.

## Runtime architecture

```text
Vercel frontend
  -> https://api.<vps-ip>.sslip.io/api/v1
  -> Caddy container :443
  -> scc-backend:8080
  -> scc-postgres:5432 on Docker network scc-net
  -> scc-minio:9000 on Docker network scc-net

Browser direct image upload/download:
  Vercel frontend -> https://storage.<vps-ip>.sslip.io -> Caddy -> scc-minio:9000
```

When a real domain is available, replace the `sslip.io` hostnames with `api.<domain>` and `storage.<domain>` in `/opt/scc-backend/.env`, then rerun `scripts/deploy-vps.sh`.

## Current stack decision

- Frontend: Vercel
- Backend API: VPS, Docker image from GHCR, Caddy HTTPS reverse proxy
- Database: PostgreSQL container on the VPS
- Object/image storage: MinIO container on the VPS
- Public hostnames without a domain: `api.<vps-ip>.sslip.io`, `storage.<vps-ip>.sslip.io`
- Not used: NeonDB, Cloudflare R2, nginx/certbot

## Recommended VPS

- Ubuntu 24.04 LTS
- 1 vCPU minimum
- 2 GB RAM recommended
- 40 GB disk recommended because PostgreSQL and MinIO data live on the VPS
- Docker installed
- Inbound ports open: `22`, `80`, `443`

## Server packages

```bash
sudo apt update
sudo apt install -y ca-certificates curl git ufw util-linux
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"
```

Log out/in after adding the user to the docker group.

`nginx` is not required. If it is installed and listening on port 80, `scripts/deploy-vps.sh` disables/stops it before starting Caddy.

## App directory and env file

Connect to the production server with:

```bash
ssh sccvps
```

```bash
sudo mkdir -p /opt/scc-backend
sudo touch /opt/scc-backend/.env
sudo chown -R "$USER":"$USER" /opt/scc-backend
chmod 600 /opt/scc-backend/.env
```

Create `/opt/scc-backend/.env` from `.env.example` and fill real values. Example for VPS IP `103.117.151.158`:

```env
API_HOST=api.103.117.151.158.sslip.io
STORAGE_HOST=storage.103.117.151.158.sslip.io
CADDY_EMAIL=admin@example.com

PORT=8080
ENV=production
CORS_ORIGINS=https://<vercel-production-domain>
JWT_SECRET=<long-random-secret>
JWT_ACCESS_TTL=15m
JWT_REFRESH_TTL=168h

POSTGRES_DB=smartcover
POSTGRES_USER=smartcover
POSTGRES_PASSWORD=<strong-postgres-password>
POSTGRES_IMAGE=postgres@sha256:<reviewed-postgres-16-alpine-digest>
DATABASE_URL=postgresql://smartcover:<strong-postgres-password>@scc-postgres:5432/smartcover?sslmode=disable

MINIO_ROOT_USER=<minio-root-user>
MINIO_ROOT_PASSWORD=<strong-minio-root-password>
MINIO_INTERNAL_ENDPOINT=scc-minio:9000
MINIO_INTERNAL_USE_SSL=false
MINIO_PUBLIC_ENDPOINT=storage.103.117.151.158.sslip.io
MINIO_PUBLIC_USE_SSL=true
MINIO_ACCESS_KEY=<minio-application-service-account-access-key>
MINIO_SECRET_KEY=<minio-application-service-account-secret-key>
MINIO_BUCKET=scc
MINIO_CONFIGURE_CORS=best-effort
MINIO_IMAGE=minio/minio@sha256:<reviewed-minio-digest>
MINIO_MC_IMAGE=minio/mc@sha256:<reviewed-minio-client-digest>
CADDY_IMAGE=caddy@sha256:<reviewed-caddy-digest>

AUTO_MIGRATE=false
SEED_DATA=false
ENABLE_PHASE2_BORROWING=false
RUN_BACKGROUND_JOBS=true
```

Notes:

- `DATABASE_URL` uses Docker service name `scc-postgres`; it is resolved inside the `scc-net` Docker network.
- URL-encode `POSTGRES_PASSWORD` inside `DATABASE_URL` if it contains reserved characters such as `+`, `/`, `@`, `:`, `%`, or `=`. Keep the plain `POSTGRES_PASSWORD` value unchanged for the PostgreSQL container.
- The deploy fails before Docker mutation unless `DATABASE_URL` targets exactly `scc-postgres:5432`, matches `POSTGRES_USER`, `POSTGRES_PASSWORD`, and `POSTGRES_DB`, and has only `sslmode=disable`. This guarantees the required predeploy dump protects the same database that migrations and the API use.
- `MINIO_INTERNAL_ENDPOINT=scc-minio:9000` is used for bucket checks, object HEAD/stat, and magic-byte inspection. It remains available when Caddy is down.
- `MINIO_PUBLIC_ENDPOINT` is used only to sign short-lived browser PUT/GET URLs and must match `STORAGE_HOST`; production requires HTTPS.
- The `scc` bucket is private (`mc anonymous set none`). Photo attach stores only an opaque relation-scoped key after verifying object existence, 1 byte–10 MiB size, metadata MIME, and actual JPEG/PNG/WebP magic bytes.
- `MINIO_CONFIGURE_CORS=best-effort` lets deployment continue if the installed MinIO/mc image pair rejects bucket CORS. Use `required` only after verifying CORS support on the VPS.
- MinIO objects are never anonymous. Authenticated API authorization returns only short-lived signed reads; uploads use relation-scoped signed PUTs.
- The production API performs a readiness-only bucket/policy check; it does not
  create the bucket or mutate bucket policy. Use
  `docs/MINIO_SERVICE_ACCOUNT_ROTATION.md` to replace temporary bootstrap/root
  application credentials with the prefix-scoped API/backup service account.
- Bucket CORS is limited to explicit HTTPS `CORS_ORIGINS` plus GET/PUT/HEAD; wildcard origins are rejected by the production deploy script.
- `CORS_ORIGINS` must list the exact Vercel production/preview origins intentionally allowed for the environment. Do not use `*` in production.
- `ENV=production`, `AUTO_MIGRATE=false`, and `SEED_DATA=false` are hard deploy guards. `/app/scc-migrate validate → check → status → up → status → version` is the only schema authority and runs as one-shot containers on the private Docker network before the API candidate starts.
- Each HTTP health attempt has a bounded curl timeout (`HEALTHCHECK_TIMEOUT_SECONDS`, default 10, maximum 300) so a half-open endpoint cannot hold the deploy lock indefinitely.
- `POSTGRES_IMAGE`, `MINIO_IMAGE`, `MINIO_MC_IMAGE`, and `CADDY_IMAGE` must be repository references ending in `@sha256:<64-hex-digest>`. Existing PostgreSQL and MinIO containers must resolve to the configured image IDs or deployment stops before migrations.
- The strict env parser writes separate short-lived Docker env files: PostgreSQL and MinIO bootstrap receive only their own root/bootstrap values, while the API/migration runtime allowlist excludes infrastructure root credentials and image controls.
- Migration one-shots and the API candidate receive `RUN_BACKGROUND_JOBS=false`; only the activated backend receives `RUN_BACKGROUND_JOBS=true`. The API binary must honor this flag so candidate health checks cannot duplicate cron work.
- Keep all passwords/secrets out of git.

## GitHub Actions secrets

Set these in the backend repository:

```text
VPS_HOST            VPS public IP or DNS name
VPS_USER            SSH user with docker access
VPS_SSH_KEY         Private SSH deploy key
VPS_PORT            Optional; defaults to 22 in workflow expressions
```

The workflow grants `packages: write` only to the image-build job. The deploy job receives a read-only `GITHUB_TOKEN`, creates a mode-`700` unpredictable remote directory, copies a mode-`600` shell-escaped env file into it, and removes the directory after deploy. The deploy script immediately unexports registry credentials from unrelated subprocesses, uses a temporary Docker credential directory for the authenticated pull, and removes it immediately afterward. No database URL or GHCR username/token repository secret is required in GitHub Actions for the current VPS-local PostgreSQL design.

CI pins the VPS ED25519 host public key in `deploy/known_hosts`. The key was verified through the existing trusted operator connection; it is public, reviewable release metadata rather than a secret. When the VPS host key is intentionally rotated, verify the new fingerprint through the provider console or another trusted channel and update this file in the same reviewed release. CI intentionally does not run `ssh-keyscan` against the deployment target because accepting a key from the same untrusted connection would not authenticate the host.

Set the optional GitHub Actions repository variable `FRONTEND_HEALTHCHECK_URL` to the production login URL when the frontend should be part of the deploy health gate. Leave it unset if a Vercel incident must not roll back an otherwise healthy backend release.

## Deployment flow

On push to `main`, `.github/workflows/deploy.yml` will:

1. Run `go vet ./...` and `go test -race ./...`.
2. Build and push both commit and convenience tags to GHCR, then select the immutable `repository@sha256:digest` returned by the build.
3. SSH into the VPS.
4. Upload `scripts/deploy-vps.sh` and the short-lived read-only GHCR credential into an unpredictable mode-`700` remote directory; fixed `/tmp` filenames are not used.
5. Acquire `/opt/scc-backend/deploy/deploy.lock`, record secret-free release metadata, pull the immutable backend and infrastructure images, reject PostgreSQL/MinIO image or persistent-volume drift, and ensure the Docker network, PostgreSQL, and MinIO are healthy. The database URL is also required to identify the same managed PostgreSQL database captured by the predeploy dump.
6. Create a required PostgreSQL custom-format dump, validate it with `pg_restore --list`, and publish it under `/opt/scc-backend/deploy/predeploy-backups/` before migrations or any candidate can run.
7. Run the target image's one-shot migration contract on `scc-net`: `validate → check → status(before) → up → status(after) → version`. Every protected stdout/stderr artifact path, result, target version, before/after version, verified version, and applied count is recorded in the release metadata.
8. Start the candidate on loopback port `18080` with background jobs disabled and require its internal health check to pass while the current backend remains available.
9. Stop and retain the current backend as `scc-backend-previous`, start the replacement as the single background-job owner, and require its final internal health check.
10. Atomically install and validate `/opt/scc-backend/Caddyfile`, retaining the previous Caddy container until public API and storage checks pass.
11. Restart Caddy and repeat the public checks to prove the Caddyfile bind source is restart-safe. Check the frontend too when `FRONTEND_HEALTHCHECK_URL` is configured.
12. Mark the release successful and remove previous containers. A failure after the switch automatically restores the previous backend, Caddy container, and Caddyfile.

Release records are stored under `/opt/scc-backend/deploy/releases/<release-id>/`. `release.env` contains only secret-free image, migration-version, artifact-path, health/rollback, timestamp, and predeploy-dump metadata. Migration stdout/stderr artifacts are mode `0600` inside the mode `0700` release directory; treat them as operator-only diagnostics because invariant failures can contain sample resource IDs.

Manual VPS command equivalent:

```bash
GHCR_IMAGE=ghcr.io/<owner>/scc-backend@sha256:<64-hex-digest> \
GHCR_USERNAME=<github-username> \
GHCR_TOKEN=<github-token-with-read-packages> \
RELEASE_ID=<unique-release-id> \
/tmp/deploy-scc-backend.sh
```

Registry deployments reject tags by default, including commit-shaped tags, because registries can move them. `ALLOW_MUTABLE_IMAGE=true` is an explicit emergency/manual exception and should not be used by CI.

For local one-off deploy without GHCR, load an image on the VPS and run:

```bash
GHCR_IMAGE=scc-backend:local-vps \
  SKIP_IMAGE_PULL=true \
  ALLOW_MUTABLE_IMAGE=true \
  /tmp/deploy-scc-backend.sh
```

`SKIP_IMAGE_PULL=true` only skips the registry pull; it does not weaken the immutable-reference guard. A local/tag reference additionally requires the explicit emergency override `ALLOW_MUTABLE_IMAGE=true`. The resolved image ID is still recorded before the candidate starts, but normal CI and routine VPS deployment must use `repository@sha256:digest`.

## Failure and rollback behavior

- Lock contention, pull failure, required `pg_dump` failure, migration validation/check/status/up/version failure, MinIO setup failure, or candidate failure stops before the current backend is switched.
- A predeploy dump that cannot be parsed by `pg_restore --list` is deleted and stops the release before migrations.
- A failure after the backend switch restores the retained previous backend automatically.
- If stopping/renaming the current backend or Caddy aborts a switch, the script restarts and checks the original container. Failure to recover that original container is recorded as `rollback_failed` with exit code `90`, not as an ordinary deploy failure.
- A Caddy start, public route, optional frontend, or Caddy restart failure also restores the previous Caddy container and Caddyfile.
- The predeploy PostgreSQL dump is retained on every failed release.
- Database restore is deliberately **not automatic**. The explicit forward migration may commit before traffic switches while the old app can still accept writes. Automatically restoring the dump would destroy those writes. Inspect the retained command artifacts and migration ledger, then use a reviewed forward fix or maintenance-window restore procedure.
- Container rollback never rolls schema back. It is safe only when every applied migration remains backward-compatible with the retained application image; otherwise keep the compatible version serving and ship a reviewed forward fix.
- A migration that is not backward-compatible still requires a planned maintenance window or an expand/migrate/contract migration process. Container rollback alone cannot make a destructive schema change safe.

## Smoke checks

```bash
curl -f http://127.0.0.1:8080/api/v1/health
curl -f https://api.103.117.151.158.sslip.io/api/v1/health
curl -f http://127.0.0.1:9000/minio/health/live
curl -f https://storage.103.117.151.158.sslip.io/minio/health/live
```

## Vercel frontend env

Set in Vercel project:

```env
NEXT_PUBLIC_API_BASE_URL=https://api.103.117.151.158.sslip.io/api/v1
```

## Backup notes

Because PostgreSQL and MinIO data are on the VPS, schedule backups for both Docker volumes:

- `scc-postgres-data`
- `scc-minio-data`

At minimum, automate `pg_dump` plus MinIO bucket sync to offsite storage before production use.

The required predeploy dump is a local release checkpoint only. It does not replace scheduled encrypted offsite backups, retention, checksum verification, MinIO backup, or restore drills.
