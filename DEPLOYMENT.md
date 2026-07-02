# VPS Backend Deployment

SCC backend production target: Ubuntu VPS running the latest Docker image from GitHub Container Registry (GHCR), behind `nginx` with HTTPS.

## Runtime architecture

```text
Vercel frontend -> https://api.<domain>/api/v1 -> nginx -> Docker container ghcr.io/<owner>/scc-backend:latest -> Neon Postgres / Cloudflare R2
```

The app container is stateless. Database migrations run in GitHub Actions before the image is deployed.

## Recommended VPS

- Ubuntu 24.04 LTS
- 1 vCPU minimum
- 2 GB RAM recommended
- 20-40 GB disk
- Docker installed
- No local Postgres required; use Neon
- No local object storage required; use Cloudflare R2

## Server packages

```bash
sudo apt update
sudo apt install -y nginx certbot python3-certbot-nginx ca-certificates curl git ufw
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"
```

Log out/in after adding the user to the docker group.

## App directory and env file

```bash
sudo mkdir -p /opt/scc-backend
sudo touch /opt/scc-backend/.env
sudo chown -R "$USER":"$USER" /opt/scc-backend
chmod 600 /opt/scc-backend/.env
```

Create `/opt/scc-backend/.env`:

```env
DATABASE_URL=postgresql://...
JWT_SECRET=<long-random-secret>
JWT_ACCESS_TTL=15m
JWT_REFRESH_TTL=720h

MINIO_ENDPOINT=<cloudflare-account-id>.r2.cloudflarestorage.com
MINIO_ACCESS_KEY=<r2-access-key-id>
MINIO_SECRET_KEY=<r2-secret-access-key>
MINIO_BUCKET=scc
MINIO_PUBLIC_URL=https://<public-r2-domain>
MINIO_USE_SSL=true

CORS_ORIGINS=https://<vercel-domain>
PORT=8080
ENV=production
AUTO_MIGRATE=false
SEED_DATA=false
```

## GitHub Actions secrets

Set these in the backend repository:

```text
DATABASE_URL        Neon production database URL for migrations
VPS_HOST            VPS public IP or DNS name
VPS_USER            SSH user with docker access
VPS_SSH_KEY         Private SSH deploy key
VPS_PORT            Optional; defaults to 22 in workflow expressions
GHCR_USERNAME       Optional if GHCR package is public; required for private pull on VPS
GHCR_TOKEN          Optional if GHCR package is public; required for private pull on VPS
```

Recommended: create a GitHub PAT for `GHCR_TOKEN` with `read:packages` only and use the GitHub username as `GHCR_USERNAME`.

## Deployment flow

On push to `main`, `.github/workflows/deploy.yml` will:

1. Run `go test ./...`.
2. Run Goose migrations from `db/migrations` against `DATABASE_URL`.
3. Build and push Docker image to GHCR as:
   - `ghcr.io/<owner>/scc-backend:latest`
   - `ghcr.io/<owner>/scc-backend:<commit-sha>`
4. SSH into the VPS.
5. Pull `latest`, replace the running container, and health check `/api/v1/health`.

Manual VPS command equivalent:

```bash
GHCR_IMAGE=ghcr.io/<owner>/scc-backend:latest \
GHCR_USERNAME=<github-username> \
GHCR_TOKEN=<github-token-with-read-packages> \
/tmp/deploy-scc-backend.sh
```

## nginx reverse proxy

Create `/etc/nginx/sites-available/scc-backend`:

```nginx
server {
    server_name api.<domain>;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Enable and HTTPS:

```bash
sudo ln -s /etc/nginx/sites-available/scc-backend /etc/nginx/sites-enabled/scc-backend
sudo nginx -t
sudo systemctl reload nginx
sudo certbot --nginx -d api.<domain>
```

## Smoke checks

```bash
curl -f http://127.0.0.1:8080/api/v1/health
curl -f https://api.<domain>/api/v1/health
```

## Vercel frontend env

Set in Vercel project:

```env
NEXT_PUBLIC_API_BASE_URL=https://api.<domain>/api/v1
```
