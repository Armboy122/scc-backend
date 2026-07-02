#!/usr/bin/env bash
set -euo pipefail

: "${GHCR_IMAGE:?GHCR_IMAGE is required}"
: "${APP_ENV_PATH:=/opt/scc-backend/.env}"
: "${APP_PORT:=8080}"
: "${HOST_PORT:=127.0.0.1:8080}"
: "${CONTAINER_NAME:=scc-backend}"
: "${DOCKER_NETWORK:=scc-net}"
: "${POSTGRES_CONTAINER:=scc-postgres}"
: "${POSTGRES_VOLUME:=scc-postgres-data}"
: "${POSTGRES_IMAGE:=postgres:16-alpine}"
: "${MINIO_CONTAINER:=scc-minio}"
: "${MINIO_VOLUME:=scc-minio-data}"
: "${MINIO_IMAGE:=minio/minio:RELEASE.2025-09-07T16-13-09Z}"
: "${MINIO_MC_IMAGE:=minio/mc:RELEASE.2025-08-13T08-35-41Z}"
: "${MINIO_API_HOST_PORT:=127.0.0.1:9000}"
: "${MINIO_CONSOLE_HOST_PORT:=127.0.0.1:9001}"
: "${MINIO_CONFIGURE_CORS:=best-effort}"
: "${CADDY_CONTAINER:=scc-caddy}"
: "${CADDY_IMAGE:=caddy:2-alpine}"
: "${CADDY_DATA_VOLUME:=scc-caddy-data}"
: "${CADDY_CONFIG_VOLUME:=scc-caddy-config}"
: "${MANAGE_CADDY:=true}"
: "${DISABLE_NGINX:=true}"
: "${SKIP_IMAGE_PULL:=false}"

if [[ ! -f "${APP_ENV_PATH}" ]]; then
  echo "env file not found: ${APP_ENV_PATH}" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
source "${APP_ENV_PATH}"
set +a

required_env=(
  DATABASE_URL
  JWT_SECRET
  POSTGRES_DB
  POSTGRES_USER
  POSTGRES_PASSWORD
  MINIO_ROOT_USER
  MINIO_ROOT_PASSWORD
  MINIO_BUCKET
  MINIO_ENDPOINT
  MINIO_ACCESS_KEY
  MINIO_SECRET_KEY
  MINIO_PUBLIC_URL
)

if [[ "${MANAGE_CADDY}" == "true" ]]; then
  required_env+=(API_HOST STORAGE_HOST)
fi

for key in "${required_env[@]}"; do
  if [[ -z "${!key:-}" ]]; then
    echo "${key} is required in ${APP_ENV_PATH}" >&2
    exit 1
  fi
done

host_port() {
  local binding="$1"
  echo "${binding##*:}"
}

container_exists() {
  docker inspect "$1" >/dev/null 2>&1
}

container_running() {
  [[ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null || true)" == "true" ]]
}

ensure_network() {
  docker network inspect "${DOCKER_NETWORK}" >/dev/null 2>&1 || docker network create "${DOCKER_NETWORK}" >/dev/null
}

ensure_container_network() {
  docker network connect "${DOCKER_NETWORK}" "$1" >/dev/null 2>&1 || true
}

wait_for_postgres() {
  for _ in $(seq 1 60); do
    if docker exec "${POSTGRES_CONTAINER}" pg_isready -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "${POSTGRES_CONTAINER} failed readiness check" >&2
  docker logs --tail 200 "${POSTGRES_CONTAINER}" >&2 || true
  exit 1
}

wait_for_minio() {
  local minio_healthcheck_url="${MINIO_HEALTHCHECK_URL:-http://127.0.0.1:$(host_port "${MINIO_API_HOST_PORT}")/minio/health/live}"
  for _ in $(seq 1 60); do
    if curl -fsS "${minio_healthcheck_url}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "${MINIO_CONTAINER} failed readiness check" >&2
  docker logs --tail 200 "${MINIO_CONTAINER}" >&2 || true
  exit 1
}

ensure_postgres() {
  docker volume create "${POSTGRES_VOLUME}" >/dev/null
  if container_exists "${POSTGRES_CONTAINER}"; then
    ensure_container_network "${POSTGRES_CONTAINER}"
    if ! container_running "${POSTGRES_CONTAINER}"; then
      docker start "${POSTGRES_CONTAINER}" >/dev/null
    fi
  else
    docker run -d \
      --name "${POSTGRES_CONTAINER}" \
      --restart unless-stopped \
      --network "${DOCKER_NETWORK}" \
      -e POSTGRES_DB="${POSTGRES_DB}" \
      -e POSTGRES_USER="${POSTGRES_USER}" \
      -e POSTGRES_PASSWORD="${POSTGRES_PASSWORD}" \
      -v "${POSTGRES_VOLUME}:/var/lib/postgresql/data" \
      "${POSTGRES_IMAGE}" >/dev/null
  fi
  wait_for_postgres
}

ensure_minio() {
  docker volume create "${MINIO_VOLUME}" >/dev/null
  if container_exists "${MINIO_CONTAINER}"; then
    ensure_container_network "${MINIO_CONTAINER}"
    if ! container_running "${MINIO_CONTAINER}"; then
      docker start "${MINIO_CONTAINER}" >/dev/null
    fi
  else
    docker run -d \
      --name "${MINIO_CONTAINER}" \
      --restart unless-stopped \
      --network "${DOCKER_NETWORK}" \
      -p "${MINIO_API_HOST_PORT}:9000" \
      -p "${MINIO_CONSOLE_HOST_PORT}:9001" \
      -e MINIO_ROOT_USER="${MINIO_ROOT_USER}" \
      -e MINIO_ROOT_PASSWORD="${MINIO_ROOT_PASSWORD}" \
      -v "${MINIO_VOLUME}:/data" \
      "${MINIO_IMAGE}" server /data --console-address ':9001' >/dev/null
  fi
  wait_for_minio
}

configure_minio_bucket() {
  local cors_file mc_script
  cors_file="$(mktemp)"
  mc_script="$(mktemp)"
  cleanup_minio_config() {
    rm -f "${cors_file}" "${mc_script}"
    trap - RETURN
  }
  trap cleanup_minio_config RETURN
  python3 - "${cors_file}" <<'PY'
import os, sys
from xml.sax.saxutils import escape
origins = [origin.strip() for origin in os.environ.get("CORS_ORIGINS", "*").split(",") if origin.strip()]
if not origins:
    origins = ["*"]
parts = ['<CORSConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">', '<CORSRule>']
for origin in origins:
    parts.append(f'<AllowedOrigin>{escape(origin)}</AllowedOrigin>')
for method in ["GET", "PUT", "POST", "HEAD"]:
    parts.append(f'<AllowedMethod>{method}</AllowedMethod>')
parts.extend([
    '<AllowedHeader>*</AllowedHeader>',
    '<ExposeHeader>ETag</ExposeHeader>',
    '<MaxAgeSeconds>3000</MaxAgeSeconds>',
    '</CORSRule>',
    '</CORSConfiguration>',
])
with open(sys.argv[1], "w", encoding="utf-8") as file:
    file.write("\n".join(parts))
PY

  cat >"${mc_script}" <<'SH'
#!/bin/sh
set -eu

minio_container="$1"
minio_user="$2"
minio_password="$3"
minio_bucket="$4"

mc alias set scc "http://${minio_container}:9000" "${minio_user}" "${minio_password}"
mc mb --ignore-existing "scc/${minio_bucket}"
mc anonymous set download "scc/${minio_bucket}"
case "${MINIO_CONFIGURE_CORS:-best-effort}" in
  false|off|skip)
    echo "Skipping MinIO CORS configuration because MINIO_CONFIGURE_CORS=${MINIO_CONFIGURE_CORS}" >&2
    ;;
  required)
    mc cors set "scc/${minio_bucket}" /tmp/cors.xml
    ;;
  best-effort|true|on|*)
    if ! mc cors set "scc/${minio_bucket}" /tmp/cors.xml; then
      echo "WARNING: MinIO CORS configuration failed; bucket and public download are ready, continuing deploy." >&2
      echo "Set MINIO_CONFIGURE_CORS=required to make this fatal after confirming the MinIO/mc version supports bucket CORS." >&2
    fi
    ;;
esac
SH
  chmod 700 "${mc_script}"

  if ! docker run --rm \
    --network "${DOCKER_NETWORK}" \
    -e "MINIO_CONFIGURE_CORS=${MINIO_CONFIGURE_CORS}" \
    -v "${cors_file}:/tmp/cors.xml:ro" \
    -v "${mc_script}:/tmp/configure-minio.sh:ro" \
    --entrypoint /bin/sh \
    "${MINIO_MC_IMAGE}" \
    /tmp/configure-minio.sh \
    "${MINIO_CONTAINER}" \
    "${MINIO_ROOT_USER}" \
    "${MINIO_ROOT_PASSWORD}" \
    "${MINIO_BUCKET}" >/dev/null; then
    return 1
  fi
}

wait_for_backend() {
  : "${HEALTHCHECK_URL:=http://127.0.0.1:$(host_port "${HOST_PORT}")/api/v1/health}"
  for _ in $(seq 1 30); do
    if curl -fsS "${HEALTHCHECK_URL}" >/dev/null; then
      return 0
    fi
    sleep 2
  done
  echo "${CONTAINER_NAME} failed health check" >&2
  docker logs --tail 200 "${CONTAINER_NAME}" >&2
  exit 1
}

stop_nginx_for_caddy() {
  if [[ "${DISABLE_NGINX}" != "true" ]]; then
    return 0
  fi
  if command -v systemctl >/dev/null 2>&1; then
    sudo systemctl disable --now nginx >/dev/null 2>&1 || true
  fi
  sudo pkill -x nginx >/dev/null 2>&1 || true
}

ensure_caddy() {
  if [[ "${MANAGE_CADDY}" != "true" ]]; then
    return 0
  fi
  stop_nginx_for_caddy
  docker volume create "${CADDY_DATA_VOLUME}" >/dev/null
  docker volume create "${CADDY_CONFIG_VOLUME}" >/dev/null

  local caddyfile
  caddyfile="$(mktemp)"
  cat >"${caddyfile}" <<EOF
{
    email ${CADDY_EMAIL:-admin@${API_HOST}}
}

${API_HOST} {
    encode zstd gzip
    reverse_proxy ${CONTAINER_NAME}:${APP_PORT}
}

${STORAGE_HOST} {
    encode zstd gzip
    request_body {
        max_size 25MB
    }
    reverse_proxy ${MINIO_CONTAINER}:9000
}
EOF

  docker rm -f "${CADDY_CONTAINER}" >/dev/null 2>&1 || true
  docker run -d \
    --name "${CADDY_CONTAINER}" \
    --restart unless-stopped \
    --network "${DOCKER_NETWORK}" \
    -p 80:80 \
    -p 443:443 \
    -v "${caddyfile}:/etc/caddy/Caddyfile:ro" \
    -v "${CADDY_DATA_VOLUME}:/data" \
    -v "${CADDY_CONFIG_VOLUME}:/config" \
    "${CADDY_IMAGE}" >/dev/null
  rm -f "${caddyfile}"
}

if [[ -n "${GHCR_USERNAME:-}" && -n "${GHCR_TOKEN:-}" ]]; then
  echo "${GHCR_TOKEN}" | docker login ghcr.io -u "${GHCR_USERNAME}" --password-stdin
fi

ensure_network
ensure_postgres
ensure_minio
configure_minio_bucket

if [[ "${SKIP_IMAGE_PULL}" != "true" ]]; then
  docker pull "${GHCR_IMAGE}"
fi

docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true

docker run -d \
  --name "${CONTAINER_NAME}" \
  --restart unless-stopped \
  --network "${DOCKER_NETWORK}" \
  --env-file "${APP_ENV_PATH}" \
  -p "${HOST_PORT}:${APP_PORT}" \
  "${GHCR_IMAGE}" >/dev/null

wait_for_backend
ensure_caddy

echo "scc-backend is healthy"
echo "api: https://${API_HOST:-<managed-elsewhere>}/api/v1/health"
echo "storage: https://${STORAGE_HOST:-<managed-elsewhere>}"
echo "postgres container: ${POSTGRES_CONTAINER}"
echo "minio container: ${MINIO_CONTAINER}"
echo "caddy container: ${CADDY_CONTAINER}"
