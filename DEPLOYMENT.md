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
sudo apt install -y ca-certificates curl git ufw
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
CORS_ORIGINS=*
JWT_SECRET=<long-random-secret>
JWT_ACCESS_TTL=15m
JWT_REFRESH_TTL=168h

POSTGRES_DB=smartcover
POSTGRES_USER=smartcover
POSTGRES_PASSWORD=<strong-postgres-password>
DATABASE_URL=postgresql://smartcover:<strong-postgres-password>@scc-postgres:5432/smartcover?sslmode=disable

MINIO_ROOT_USER=<minio-root-user>
MINIO_ROOT_PASSWORD=<strong-minio-root-password>
MINIO_ENDPOINT=storage.103.117.151.158.sslip.io
MINIO_ACCESS_KEY=<minio-root-user>
MINIO_SECRET_KEY=<strong-minio-root-password>
MINIO_BUCKET=scc
MINIO_PUBLIC_URL=https://storage.103.117.151.158.sslip.io
MINIO_USE_SSL=true
MINIO_CONFIGURE_CORS=best-effort

AUTO_MIGRATE=true
SEED_DATA=false
```

Notes:

- `DATABASE_URL` uses Docker service name `scc-postgres`; it is resolved inside the `scc-net` Docker network.
- URL-encode `POSTGRES_PASSWORD` inside `DATABASE_URL` if it contains reserved characters such as `+`, `/`, `@`, `:`, `%`, or `=`. Keep the plain `POSTGRES_PASSWORD` value unchanged for the PostgreSQL container.
- `MINIO_ENDPOINT` uses the public HTTPS storage hostname because presigned upload URLs are returned to the browser.
- `MINIO_PUBLIC_URL` should match the same public storage hostname.
- `MINIO_CONFIGURE_CORS=best-effort` lets deployment continue if the installed MinIO/mc image pair rejects bucket CORS. Use `required` only after verifying CORS support on the VPS.
- `CORS_ORIGINS=*` is allowed during bootstrap; the backend echoes the request origin so credentialed requests work. Replace it with the final Vercel URL later.
- `AUTO_MIGRATE=true` is intentional for this stack because PostgreSQL is private on the VPS.
- Keep all passwords/secrets out of git.

## GitHub Actions secrets

Set these in the backend repository:

```text
VPS_HOST            VPS public IP or DNS name
VPS_USER            SSH user with docker access
VPS_SSH_KEY         Private SSH deploy key
VPS_PORT            Optional; defaults to 22 in workflow expressions
```

The workflow uses the built-in GitHub Actions actor and `GITHUB_TOKEN` for GHCR push/pull credentials, copies them to the VPS in a temporary `chmod 600` env file, then removes that file after deploy. No database URL or GHCR username/token repository secret is required in GitHub Actions for the current VPS-local PostgreSQL design.

## Deployment flow

On push to `main`, `.github/workflows/deploy.yml` will:

1. Run `go test ./...`.
2. Build and push Docker image to GHCR.
3. SSH into the VPS.
4. Upload `scripts/deploy-vps.sh`.
5. Ensure Docker network, PostgreSQL container, MinIO container, bucket, backend container, and Caddy container exist.
6. Configure MinIO bucket public download and best-effort bucket CORS.
7. Disable nginx if needed so Caddy can bind ports 80/443.
8. Health check `/api/v1/health`.

Manual VPS command equivalent:

```bash
GHCR_IMAGE=ghcr.io/<owner>/scc-backend:latest \
GHCR_USERNAME=<github-username> \
GHCR_TOKEN=<github-token-with-read-packages> \
/tmp/deploy-scc-backend.sh
```

For local one-off deploy without GHCR, load an image on the VPS and run:

```bash
GHCR_IMAGE=scc-backend:local-vps SKIP_IMAGE_PULL=true /tmp/deploy-scc-backend.sh
```

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
