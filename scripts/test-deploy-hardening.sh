#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

bin_dir="${tmpdir}/bin"
mkdir -p "${bin_dir}"
log_file="${tmpdir}/docker.log"

cat >"${bin_dir}/docker" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'docker' >> "${FAKE_DOCKER_LOG}"
for arg in "$@"; do
  printf ' %q' "$arg" >> "${FAKE_DOCKER_LOG}"
done
printf '\n' >> "${FAKE_DOCKER_LOG}"

case "${1:-}" in
  exec)
    exit 0
    ;;
  inspect)
    if [[ "${2:-}" == "-f" ]]; then
      printf 'false\n'
      exit 0
    fi
    exit 1
    ;;
  volume|network|run|rm|pull|logs|start)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
SH
chmod +x "${bin_dir}/docker"

cat >"${bin_dir}/curl" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "${bin_dir}/curl"

cat >"${bin_dir}/sudo" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "${bin_dir}/sudo"

app_env="${tmpdir}/app.env"
cat >"${app_env}" <<'ENV'
DATABASE_URL=postgresql://smartcover:pw@scc-postgres:5432/smartcover?sslmode=disable
JWT_SECRET=test-secret
POSTGRES_DB=smartcover
POSTGRES_USER=smartcover
POSTGRES_PASSWORD=postgres-password
MINIO_ROOT_USER=minio-root
MINIO_ROOT_PASSWORD='pa:ss@word/%with;chars'
MINIO_BUCKET='scc;touch /tmp/should-not-exist'
MINIO_ENDPOINT=https://storage.example.test
MINIO_ACCESS_KEY=minio-root
MINIO_SECRET_KEY='pa:ss@word/%with;chars'
MINIO_PUBLIC_URL=https://storage.example.test
MINIO_CONFIGURE_CORS=best-effort
API_HOST=api.example.test
STORAGE_HOST=storage.example.test
CORS_ORIGINS=https://app.example.test
ENV

PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_LOG="${log_file}" \
APP_ENV_PATH="${app_env}" \
GHCR_IMAGE="ghcr.io/example/scc-backend:latest" \
MANAGE_CADDY=false \
SKIP_IMAGE_PULL=true \
"${repo_root}/scripts/deploy-vps.sh" >/dev/null

if grep -q 'MC_HOST_scc=http' "${log_file}"; then
  echo "FAIL: MinIO credentials were embedded in MC_HOST_scc URL" >&2
  exit 1
fi

if grep -q ' sh -c ' "${log_file}"; then
  echo "FAIL: MinIO bucket commands still use string-built sh -c" >&2
  exit 1
fi

if grep -q "GHCR_TOKEN='" "${repo_root}/.github/workflows/deploy.yml"; then
  echo "FAIL: GHCR_TOKEN is still passed on the remote SSH command line" >&2
  exit 1
fi

if ! grep -q 'MINIO_CONFIGURE_CORS:=best-effort' "${repo_root}/scripts/deploy-vps.sh"; then
  echo "FAIL: deploy script must default MinIO CORS setup to best-effort" >&2
  exit 1
fi

if ! grep -q 'WARNING: MinIO CORS configuration failed' "${repo_root}/scripts/deploy-vps.sh"; then
  echo "FAIL: deploy script must keep MinIO bucket CORS failure non-fatal by default" >&2
  exit 1
fi

if ! grep -q 'MINIO_CONFIGURE_CORS=required' "${repo_root}/scripts/deploy-vps.sh"; then
  echo "FAIL: deploy script should document required mode for CORS hard-fail" >&2
  exit 1
fi

echo "deploy hardening checks passed"
