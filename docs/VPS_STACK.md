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
| `scc-caddy` | `caddy:2-alpine` | Public HTTPS reverse proxy |
| `scc-backend` | `ghcr.io/<owner>/scc-backend:latest` or loaded local image | Go API |
| `scc-postgres` | `postgres:16-alpine` | PostgreSQL data store |
| `scc-minio` | `minio/minio` | S3-compatible object store |

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
MINIO_ENDPOINT=storage.<domain>
MINIO_PUBLIC_URL=https://storage.<domain>
```

Then rerun `scripts/deploy-vps.sh`.

## Migration policy

The PostgreSQL container is private on the VPS, so GitHub Actions does not run database migrations directly.

Current production policy:

```env
AUTO_MIGRATE=true
SEED_DATA=false
```

This lets the Go app apply GORM schema migration at startup. If the project later needs stricter migration control, replace this with a VPS-side Goose migration step that runs on the same Docker network before starting the backend.

## Security notes

- Do not commit `/opt/scc-backend/.env` or real secrets.
- Keep `POSTGRES_PASSWORD` plain for the PostgreSQL container, but URL-encode it inside `DATABASE_URL` when it contains URL-reserved characters.
- Keep public API/storage behind Caddy HTTPS.
- MinIO bucket CORS is best-effort during deploy by default (`MINIO_CONFIGURE_CORS=best-effort`) so an image-pair CORS incompatibility does not block API deployment.
- `CORS_ORIGINS=*` is acceptable for bootstrap only because the backend echoes the request origin; set the final Vercel URL before real production.
- Rotate `JWT_SECRET`, `POSTGRES_PASSWORD`, and MinIO root credentials before real production use.
- Add offsite backups before relying on VPS-local state.
