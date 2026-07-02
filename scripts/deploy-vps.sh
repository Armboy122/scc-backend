#!/usr/bin/env bash
set -euo pipefail

: "${GHCR_IMAGE:?GHCR_IMAGE is required}"
: "${APP_ENV_PATH:=/opt/scc-backend/.env}"
: "${APP_PORT:=8080}"
: "${HOST_PORT:=127.0.0.1:8080}"
: "${CONTAINER_NAME:=scc-backend}"

if [[ -n "${GHCR_USERNAME:-}" && -n "${GHCR_TOKEN:-}" ]]; then
  echo "${GHCR_TOKEN}" | docker login ghcr.io -u "${GHCR_USERNAME}" --password-stdin
fi

docker pull "${GHCR_IMAGE}"

docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true

docker run -d \
  --name "${CONTAINER_NAME}" \
  --restart unless-stopped \
  --env-file "${APP_ENV_PATH}" \
  -p "${HOST_PORT}:${APP_PORT}" \
  "${GHCR_IMAGE}"

for i in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${APP_PORT}/api/v1/health" >/dev/null; then
    echo "scc-backend is healthy"
    exit 0
  fi
  sleep 2
done

echo "scc-backend failed health check" >&2
docker logs --tail 200 "${CONTAINER_NAME}" >&2
exit 1
