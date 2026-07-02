# SCC Backend

Smart Cover Connect backend API written in Go.

## Target runtime

Production backend runs on a VPS as a native Go binary managed by `systemd`, behind `nginx` with HTTPS.

```text
Vercel frontend -> https://api.<domain>/api/v1 -> nginx -> scc-backend systemd service -> Neon Postgres / Cloudflare R2
```

Docker is intentionally not part of the current dev/deploy flow. Re-add a Dockerfile later only if the VPS deployment moves to containers.

## Local development

1. Copy env template:

```bash
cp .env.example .env.local
```

2. Fill `.env.local` with Neon dev DB and R2 dev bucket values.

3. Export env and run API:

```bash
set -a
source .env.local
set +a
go run ./cmd/api
```

4. Health check:

```bash
curl http://127.0.0.1:8080/api/v1/health
```

## Test

```bash
go test ./...
```

## Required env

See `.env.example`.

Current storage env names use `MINIO_*` because the code uses the MinIO S3-compatible client. For Cloudflare R2, set these to R2 values.
