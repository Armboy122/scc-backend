# VPS Backend Deployment

SCC backend production target: small Ubuntu VPS running a native Go binary with `systemd` and `nginx`.

## Recommended VPS for first month

- Ubuntu 24.04 LTS
- 1 vCPU minimum
- 2 GB RAM recommended
- 20-40 GB disk
- No local Postgres required; use Neon
- No local object storage required; use Cloudflare R2

## Server packages

```bash
sudo apt update
sudo apt install -y nginx certbot python3-certbot-nginx ca-certificates curl git
```

Install Go on the server or build locally/CI and upload the binary. For simple first deploy, build on the VPS.

## App directory

```bash
sudo useradd --system --home /opt/scc-backend --shell /usr/sbin/nologin scc || true
sudo mkdir -p /opt/scc-backend
sudo chown -R "$USER":scc /opt/scc-backend
```

## Build

```bash
git clone https://github.com/Armboy122/scc-backend.git /opt/scc-backend/src
cd /opt/scc-backend/src
go test ./...
go build -o /opt/scc-backend/scc-api ./cmd/api
sudo chown scc:scc /opt/scc-backend/scc-api
```

## Environment file

Create `/etc/scc-backend.env`:

```env
DATABASE_URL=postgresql://...
JWT_SECRET=change-me
JWT_ACCESS_TTL=15m
JWT_REFRESH_TTL=720h

MINIO_ENDPOINT=<cloudflare-account-id>.r2.cloudflarestorage.com
MINIO_ACCESS_KEY=
MINIO_SECRET_KEY=
MINIO_BUCKET=
MINIO_PUBLIC_URL=https://<public-r2-domain-or-worker>
MINIO_USE_SSL=true

CORS_ORIGINS=https://<vercel-domain>
PORT=8080
ENV=production
SEED_DATA=false
```

Secure it:

```bash
sudo chown root:scc /etc/scc-backend.env
sudo chmod 640 /etc/scc-backend.env
```

## systemd service

Create `/etc/systemd/system/scc-backend.service`:

```ini
[Unit]
Description=Smart Cover Connect Backend
After=network-online.target
Wants=network-online.target

[Service]
User=scc
Group=scc
WorkingDirectory=/opt/scc-backend
EnvironmentFile=/etc/scc-backend.env
ExecStart=/opt/scc-backend/scc-api
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/scc-backend

[Install]
WantedBy=multi-user.target
```

Enable:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now scc-backend
sudo systemctl status scc-backend --no-pager
journalctl -u scc-backend -f
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
