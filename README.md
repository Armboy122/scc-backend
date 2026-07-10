# SCC Backend

Smart Cover Connect backend API written in Go.

## Target runtime

Production backend runs on the VPS as Docker containers behind **Caddy** with automatic HTTPS.

```text
Vercel frontend
  -> https://api.<vps-ip>.sslip.io/api/v1 or https://api.<real-domain>/api/v1
  -> Caddy container
  -> Docker scc-backend container
  -> Docker PostgreSQL container on the same VPS
  -> Docker MinIO container on the same VPS
```

Current stack decision:

| Layer | Target |
|---|---|
| Frontend | Vercel |
| Backend API | VPS via Docker/GHCR, reachable through Caddy HTTPS |
| Database | PostgreSQL container on the VPS |
| Image/object storage | Private MinIO bucket; internal verification on Docker network, short-lived signed browser access through Caddy HTTPS |
| Temporary hostnames | `api.<vps-ip>.sslip.io`, `storage.<vps-ip>.sslip.io` |

NeonDB and Cloudflare R2 are not part of the current runtime stack.

## Local development

1. Copy env template:

```bash
cp .env.example .env.local
```

2. Fill `.env.local` with a local or VPS PostgreSQL DSN and MinIO values. Keep real secrets local only.

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

Production VPS env lives at `/opt/scc-backend/.env`. The deploy script uses that file to run PostgreSQL, MinIO, Caddy, and the backend container on a shared Docker network.
