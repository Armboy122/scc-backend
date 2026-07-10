#!/usr/bin/env bash
set -euo pipefail

: "${RESTORE_STATE_DIR:=/opt/scc-backend/restore}"
: "${RESTORE_LOCK_PATH:=${RESTORE_STATE_DIR}/.restore.lock}"
: "${RESTORE_ID:=$(date -u +%Y%m%dT%H%M%SZ)}"
: "${POSTGRES_IMAGE:=}"
: "${MINIO_IMAGE:=}"
: "${MINIO_MC_IMAGE:=}"
: "${KEEP_SCRATCH:=true}"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
backup_dir=""
target=scratch
live_confirmation=""
scratch_network="scc-restore-net-${RESTORE_ID}"
scratch_postgres_container="scc-restore-postgres-${RESTORE_ID}"
scratch_minio_container="scc-restore-minio-${RESTORE_ID}"
scratch_postgres_volume="scc-restore-postgres-${RESTORE_ID}"
scratch_minio_volume="scc-restore-minio-${RESTORE_ID}"
scratch_bucket=restore
scratch_created=false
restore_succeeded=false
minio_env_file=""
mc_script=""

umask 077

usage() {
  echo "usage: $0 --backup DIR [--target scratch|live] [--confirm-live-overwrite ERASE_LIVE_SCC_DATA]" >&2
}

cleanup_scratch() {
  rm -f -- "${minio_env_file}" "${mc_script}"
  if [[ "${scratch_created}" == "true" && ( "${restore_succeeded}" != "true" || "${KEEP_SCRATCH}" != "true" ) ]]; then
    docker rm -f "${scratch_postgres_container}" >/dev/null 2>&1 || true
    docker rm -f "${scratch_minio_container}" >/dev/null 2>&1 || true
    docker volume rm "${scratch_postgres_volume}" >/dev/null 2>&1 || true
    docker volume rm "${scratch_minio_volume}" >/dev/null 2>&1 || true
    docker network rm "${scratch_network}" >/dev/null 2>&1 || true
  fi
}
trap cleanup_scratch EXIT

while [[ $# -gt 0 ]]; do
  case "$1" in
    --backup)
      [[ $# -ge 2 ]] || { usage; exit 64; }
      backup_dir="$2"
      shift 2
      ;;
    --target)
      [[ $# -ge 2 ]] || { usage; exit 64; }
      target="$2"
      shift 2
      ;;
    --confirm-live-overwrite)
      [[ $# -ge 2 ]] || { usage; exit 64; }
      live_confirmation="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      exit 64
      ;;
  esac
done

if [[ -z "${backup_dir}" || "${backup_dir}" != /* ]]; then
  usage
  exit 64
fi
if [[ "${target}" == "live" ]]; then
  if [[ "${live_confirmation}" != "ERASE_LIVE_SCC_DATA" ]]; then
    echo "live restore refused: explicit --confirm-live-overwrite ERASE_LIVE_SCC_DATA is required" >&2
    exit 64
  fi
  echo "live restore is intentionally not implemented in this version; restore to scratch and review the result" >&2
  exit 78
fi
if [[ "${target}" != "scratch" ]]; then
  echo "restore target must be scratch or live" >&2
  exit 64
fi
if [[ ! "${RESTORE_ID}" =~ ^[0-9]{8}T[0-9]{6}Z$ ]]; then
  echo "RESTORE_ID must use UTC YYYYmmddTHHMMSSZ format" >&2
  exit 64
fi
for image_ref in "${POSTGRES_IMAGE}" "${MINIO_IMAGE}" "${MINIO_MC_IMAGE}"; do
  if [[ ! "${image_ref}" =~ ^[^[:space:]@]+@sha256:[[:xdigit:]]{64}$ ]]; then
    echo "POSTGRES_IMAGE, MINIO_IMAGE, and MINIO_MC_IMAGE must use immutable sha256 digest references" >&2
    exit 1
  fi
done
for command in docker flock mktemp python3; do
  command -v "${command}" >/dev/null 2>&1 || { echo "required command not found: ${command}" >&2; exit 1; }
done
if [[ "${RESTORE_STATE_DIR}" != /* || "${RESTORE_LOCK_PATH}" != /* ]]; then
  echo "RESTORE_STATE_DIR and RESTORE_LOCK_PATH must be absolute paths" >&2
  exit 1
fi

mkdir -p "${RESTORE_STATE_DIR}"
chmod 0700 "${RESTORE_STATE_DIR}"
exec 9>"${RESTORE_LOCK_PATH}"
if ! flock -n 9; then
  echo "another SCC restore is already running" >&2
  exit 1
fi

POSTGRES_IMAGE="${POSTGRES_IMAGE}" "${script_dir}/verify-backup.sh" --backup "${backup_dir}"

for container in "${scratch_postgres_container}" "${scratch_minio_container}"; do
  if docker inspect "${container}" >/dev/null 2>&1; then
    echo "scratch container already exists: ${container}" >&2
    exit 1
  fi
done
if docker network inspect "${scratch_network}" >/dev/null 2>&1; then
  echo "scratch network already exists: ${scratch_network}" >&2
  exit 1
fi
for volume in "${scratch_postgres_volume}" "${scratch_minio_volume}"; do
  if docker volume inspect "${volume}" >/dev/null 2>&1; then
    echo "scratch volume already exists: ${volume}" >&2
    exit 1
  fi
done

scratch_created=true
docker network create "${scratch_network}" >/dev/null
docker volume create "${scratch_postgres_volume}" >/dev/null
docker volume create "${scratch_minio_volume}" >/dev/null

docker run -d \
  --name "${scratch_postgres_container}" \
  --restart no \
  --network "${scratch_network}" \
  -e POSTGRES_DB=restore \
  -e POSTGRES_USER=restore \
  -e POSTGRES_HOST_AUTH_METHOD=trust \
  -v "${scratch_postgres_volume}:/var/lib/postgresql/data" \
  "${POSTGRES_IMAGE}" >/dev/null

scratch_minio_user=restoreadmin
scratch_minio_password="$(python3 - <<'PY'
import secrets
print(secrets.token_urlsafe(32))
PY
)"
minio_env_file="$(mktemp "${RESTORE_STATE_DIR}/.minio-env.XXXXXX")"
{
  printf 'MINIO_ROOT_USER=%s\n' "${scratch_minio_user}"
  printf 'MINIO_ROOT_PASSWORD=%s\n' "${scratch_minio_password}"
} >"${minio_env_file}"
chmod 0600 "${minio_env_file}"
docker run -d \
  --name "${scratch_minio_container}" \
  --restart no \
  --network "${scratch_network}" \
  --env-file "${minio_env_file}" \
  -v "${scratch_minio_volume}:/data" \
  "${MINIO_IMAGE}" server /data >/dev/null
rm -f -- "${minio_env_file}"
minio_env_file=""

for _ in $(seq 1 60); do
  if docker exec "${scratch_postgres_container}" pg_isready --username restore --dbname restore >/dev/null 2>&1; then
    postgres_ready=true
    break
  fi
  sleep 2
done
if [[ "${postgres_ready:-false}" != "true" ]]; then
  echo "scratch PostgreSQL failed readiness check" >&2
  exit 1
fi

if ! docker exec -i "${scratch_postgres_container}" \
  pg_restore \
  --username restore \
  --dbname restore \
  --no-owner \
  --no-acl <"${backup_dir}/postgres.dump"; then
  echo "scratch PostgreSQL restore failed" >&2
  exit 1
fi
docker exec "${scratch_postgres_container}" psql --username restore --dbname restore --tuples-only --command 'SELECT 1' >/dev/null

expected_object_count="$(python3 - "${backup_dir}/metadata.env" <<'PY'
import re, sys
value = None
for line in open(sys.argv[1], encoding="utf-8"):
    if line.startswith("object_count="):
        value = line.rstrip("\n").split("=", 1)[1]
if value is None or not re.fullmatch(r"\d+", value):
    raise SystemExit("invalid object_count metadata")
print(value)
PY
)"
mc_script="$(mktemp "${RESTORE_STATE_DIR}/.restore-minio.XXXXXX")"
cat >"${mc_script}" <<'SH'
#!/bin/sh
set -eu
minio_container="$1"
bucket="$2"
expected_count="$3"
IFS= read -r access_key
IFS= read -r secret_key
attempt=0
until mc alias set scratch "http://${minio_container}:9000" "${access_key}" "${secret_key}" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  [ "${attempt}" -lt 60 ] || exit 1
  sleep 2
done
mc mb --ignore-existing "scratch/${bucket}" >/dev/null
mc mirror --overwrite /backup/objects "scratch/${bucket}" >/dev/null
actual_count="$(mc ls --recursive --json "scratch/${bucket}" | wc -l | tr -d ' ')"
[ "${actual_count}" = "${expected_count}" ]
SH
chmod 0700 "${mc_script}"
if ! printf '%s\n%s\n' "${scratch_minio_user}" "${scratch_minio_password}" | docker run --rm -i \
  --user "$(id -u):$(id -g)" \
  --network "${scratch_network}" \
  -e MC_CONFIG_DIR=/tmp/mc \
  -v "${backup_dir}/objects:/backup/objects:ro" \
  -v "${mc_script}:/tmp/restore-minio.sh:ro" \
  --entrypoint /bin/sh \
  "${MINIO_MC_IMAGE}" \
  /tmp/restore-minio.sh "${scratch_minio_container}" "${scratch_bucket}" "${expected_object_count}"; then
  echo "scratch MinIO restore or object-count verification failed" >&2
  exit 1
fi
rm -f -- "${mc_script}"
mc_script=""

restore_succeeded=true
echo "scratch restore verified"
echo "PostgreSQL container: ${scratch_postgres_container}"
echo "MinIO container: ${scratch_minio_container}"
echo "scratch network: ${scratch_network}"
if [[ "${KEEP_SCRATCH}" == "true" ]]; then
  echo "scratch resources were retained for inspection; see BACKUP_RESTORE.md for cleanup commands"
else
  echo "scratch resources will be removed after verification"
fi
