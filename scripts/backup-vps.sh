#!/usr/bin/env bash
set -euo pipefail

: "${APP_ENV_PATH:=/opt/scc-backend/.env}"
: "${BACKUP_ROOT:=/opt/scc-backend/backups}"
: "${BACKUP_LOCK_PATH:=${BACKUP_ROOT}/.backup.lock}"
: "${POSTGRES_CONTAINER:=scc-postgres}"
: "${MINIO_CONTAINER:=scc-minio}"
: "${DOCKER_NETWORK:=scc-net}"
: "${MINIO_MC_IMAGE:=}"
: "${BACKUP_ID:=$(date -u +%Y%m%dT%H%M%SZ)}"
: "${RETENTION_DAILY:=7}"
: "${RETENTION_WEEKLY:=4}"
: "${RETENTION_MONTHLY:=6}"
: "${OFFSITE_HOOK:=}"
: "${OFFSITE_REQUIRED:=false}"

umask 077
staging_dir=""
completed_dir="${BACKUP_ROOT}/${BACKUP_ID}"

cleanup_staging() {
  if [[ -n "${staging_dir}" && -d "${staging_dir}" ]]; then
    rm -rf -- "${staging_dir}"
  fi
}
trap cleanup_staging EXIT

require_command() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "required command not found: $1" >&2
    return 1
  }
}

is_immutable_image_ref() {
  [[ "$1" =~ ^[^[:space:]@]+@sha256:[[:xdigit:]]{64}$ ]]
}

load_protected_app_env() {
  local path="${APP_ENV_PATH}" mode raw line key value quote seen_key
  local seen_keys=(__backup_no_env_key__)
  if [[ "${path}" != /* || ! -f "${path}" || -L "${path}" || ! -r "${path}" ]]; then
    echo "APP_ENV_PATH must be an absolute, readable, regular non-symlink file" >&2
    return 1
  fi
  mode="$(python3 - "${path}" <<'PY'
import os, stat, sys
print(oct(stat.S_IMODE(os.stat(sys.argv[1]).st_mode)))
PY
)"
  if [[ ! "${mode}" =~ ^0o[0-7]+$ ]] || (( 8#${mode#0o} & 8#077 )); then
    echo "APP_ENV_PATH must not be readable or writable by group/other" >&2
    return 1
  fi

  while IFS= read -r raw || [[ -n "${raw}" ]]; do
    quote=""
    [[ "${raw}" != *[[:cntrl:]]* ]] || return 1
    line="${raw#"${raw%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    [[ -z "${line}" || "${line}" == \#* ]] && continue
    if [[ "${line}" =~ ^export[[:space:]]+ ]]; then
      line="${line#export}"
      line="${line#"${line%%[![:space:]]*}"}"
    fi
    [[ "${line}" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || return 1
    key="${BASH_REMATCH[1]}"
    value="${BASH_REMATCH[2]}"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    for seen_key in "${seen_keys[@]}"; do
      [[ "${seen_key}" != "${key}" ]] || return 1
    done
    seen_keys+=("${key}")
    if [[ "${value}" == \'* || "${value}" == \"* ]]; then
      [[ "${#value}" -ge 2 ]] || return 1
      quote="${value:0:1}"
      [[ "${value: -1}" == "${quote}" ]] || return 1
      value="${value:1:${#value}-2}"
      [[ "${value}" != *"${quote}"* ]] || return 1
    elif [[ "${value}" == *[[:space:]]* ]]; then
      return 1
    fi
    [[ "${value}" != *[[:cntrl:]]* ]] || return 1
    if [[ "${quote}" == "\"" && ( "${value}" == *'$'* || "${value}" == *'`'* || "${value}" == *'\'* ) ]]; then
      return 1
    fi
    if [[ -z "${quote}" && ( "${value}" == *'$'* || "${value}" == *'`'* || "${value}" == *'\'* || "${value}" == *';'* || "${value}" == *'&'* || "${value}" == *'|'* || "${value}" == *'<'* || "${value}" == *'>'* || "${value}" == *'('* || "${value}" == *')'* ) ]]; then
      return 1
    fi
    case "${key}" in
      POSTGRES_DB|POSTGRES_USER|MINIO_ACCESS_KEY|MINIO_SECRET_KEY|MINIO_BUCKET|MINIO_MC_IMAGE)
        printf -v "${key}" '%s' "${value}"
        export -n "${key}" 2>/dev/null || true
        ;;
    esac
  done <"${path}"
}

write_object_inventory() {
  python3 - "${staging_dir}" <<'PY'
import json, os, sys
root = os.path.abspath(sys.argv[1])
objects = os.path.join(root, "objects")
entries = []
for current, dirs, files in os.walk(objects, followlinks=False):
    dirs.sort()
    files.sort()
    for name in files:
        full = os.path.join(current, name)
        if os.path.islink(full) or not os.path.isfile(full):
            raise SystemExit(f"object backup contains unsupported entry: {full}")
        entries.append({
            "path": os.path.relpath(full, objects).replace(os.sep, "/"),
            "size": os.path.getsize(full),
        })
entries.sort(key=lambda item: item["path"])
with open(os.path.join(root, "object-inventory.jsonl"), "w", encoding="utf-8") as output:
    for entry in entries:
        output.write(json.dumps(entry, ensure_ascii=False, sort_keys=True, separators=(",", ":")) + "\n")
print(len(entries))
PY
}

write_sha256_manifest() {
  python3 - "${staging_dir}" <<'PY'
import hashlib, json, os, sys
root = os.path.abspath(sys.argv[1])
managed = ["postgres.dump", "metadata.env", "object-inventory.jsonl"]
objects = os.path.join(root, "objects")
for current, dirs, files in os.walk(objects, followlinks=False):
    dirs.sort()
    files.sort()
    for name in files:
        full = os.path.join(current, name)
        if os.path.islink(full) or not os.path.isfile(full):
            raise SystemExit(f"backup contains unsupported entry: {full}")
        managed.append(os.path.relpath(full, root).replace(os.sep, "/"))
managed.sort()
manifest = os.path.join(root, "SHA256SUMS.jsonl")
with open(manifest, "w", encoding="utf-8") as output:
    for relative in managed:
        full = os.path.join(root, relative)
        digest = hashlib.sha256()
        with open(full, "rb") as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b""):
                digest.update(chunk)
        output.write(json.dumps({
            "path": relative,
            "sha256": digest.hexdigest(),
            "size": os.path.getsize(full),
        }, sort_keys=True, separators=(",", ":")) + "\n")
PY
}

sha256_file() {
  python3 - "$1" <<'PY'
import hashlib, sys
digest = hashlib.sha256()
with open(sys.argv[1], "rb") as source:
    for chunk in iter(lambda: source.read(1024 * 1024), b""):
        digest.update(chunk)
print(digest.hexdigest())
PY
}

apply_retention() {
  python3 - "${BACKUP_ROOT}" "${RETENTION_DAILY}" "${RETENTION_WEEKLY}" "${RETENTION_MONTHLY}" <<'PY'
import datetime as dt, os, re, shutil, sys
root = os.path.abspath(sys.argv[1])
limits = [int(value) for value in sys.argv[2:5]]
if any(value < 0 for value in limits):
    raise SystemExit("retention values must be non-negative")
pattern = re.compile(r"^(\d{8}T\d{6}Z)$")
backups = []
for name in os.listdir(root):
    match = pattern.match(name)
    path = os.path.join(root, name)
    if not match or os.path.islink(path) or not os.path.isdir(path) or not os.path.isfile(os.path.join(path, ".complete")):
        continue
    timestamp = dt.datetime.strptime(name, "%Y%m%dT%H%M%SZ").replace(tzinfo=dt.timezone.utc)
    backups.append((timestamp, name, path))
backups.sort(reverse=True)
keep = set()
if backups:
    keep.add(backups[0][1])
selectors = [
    (limits[0], lambda value: value.date()),
    (limits[1], lambda value: value.isocalendar()[:2]),
    (limits[2], lambda value: (value.year, value.month)),
]
for limit, selector in selectors:
    seen = set()
    for timestamp, name, _ in backups:
        key = selector(timestamp)
        if key in seen:
            continue
        seen.add(key)
        if len(seen) <= limit:
            keep.add(name)
for _, name, path in backups:
    if name not in keep:
        shutil.rmtree(path)
PY
}

for command in docker flock mktemp python3; do
  require_command "${command}"
done
if [[ "${APP_ENV_PATH}" != /* || "${BACKUP_ROOT}" != /* || "${BACKUP_LOCK_PATH}" != /* ]]; then
  echo "APP_ENV_PATH, BACKUP_ROOT, and BACKUP_LOCK_PATH must be absolute paths" >&2
  exit 1
fi
if [[ ! "${BACKUP_ID}" =~ ^[0-9]{8}T[0-9]{6}Z$ ]]; then
  echo "BACKUP_ID must use UTC YYYYmmddTHHMMSSZ format" >&2
  exit 1
fi
for value in "${RETENTION_DAILY}" "${RETENTION_WEEKLY}" "${RETENTION_MONTHLY}"; do
  [[ "${value}" =~ ^[0-9]+$ ]] || { echo "retention values must be non-negative integers" >&2; exit 1; }
done
if [[ "${OFFSITE_REQUIRED}" != "true" && "${OFFSITE_REQUIRED}" != "false" ]]; then
  echo "OFFSITE_REQUIRED must be true or false" >&2
  exit 1
fi
if [[ "${OFFSITE_REQUIRED}" == "true" && -z "${OFFSITE_HOOK}" ]]; then
  echo "OFFSITE_REQUIRED=true requires OFFSITE_HOOK" >&2
  exit 1
fi

unset POSTGRES_DB POSTGRES_USER MINIO_ACCESS_KEY MINIO_SECRET_KEY MINIO_BUCKET MINIO_MC_IMAGE
load_protected_app_env
: "${POSTGRES_DB:?POSTGRES_DB is required in APP_ENV_PATH}"
: "${POSTGRES_USER:?POSTGRES_USER is required in APP_ENV_PATH}"
: "${MINIO_ACCESS_KEY:?MINIO_ACCESS_KEY is required in APP_ENV_PATH}"
: "${MINIO_SECRET_KEY:?MINIO_SECRET_KEY is required in APP_ENV_PATH}"
: "${MINIO_BUCKET:?MINIO_BUCKET is required in APP_ENV_PATH}"
: "${MINIO_MC_IMAGE:?MINIO_MC_IMAGE is required in APP_ENV_PATH}"
if ! is_immutable_image_ref "${MINIO_MC_IMAGE}"; then
  echo "MINIO_MC_IMAGE must use an immutable sha256 digest reference" >&2
  exit 1
fi
for value in "${POSTGRES_CONTAINER}" "${POSTGRES_DB}" "${POSTGRES_USER}" "${MINIO_CONTAINER}" "${MINIO_BUCKET}" "${DOCKER_NETWORK}"; do
  if [[ "${value}" == *[[:cntrl:]]* ]]; then
    echo "backup identifiers cannot contain control characters" >&2
    exit 1
  fi
done
if [[ ! "${POSTGRES_CONTAINER}" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ || ! "${MINIO_CONTAINER}" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ || ! "${DOCKER_NETWORK}" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]; then
  echo "Docker container and network names contain unsupported characters" >&2
  exit 1
fi
if [[ ! "${MINIO_BUCKET}" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]]; then
  echo "MINIO_BUCKET must be a 3-63 character DNS-style bucket name" >&2
  exit 1
fi
if [[ "${MINIO_ACCESS_KEY}" == *$'\n'* || "${MINIO_SECRET_KEY}" == *$'\n'* ]]; then
  echo "MinIO credentials cannot contain newlines" >&2
  exit 1
fi

mkdir -p "${BACKUP_ROOT}"
chmod 0700 "${BACKUP_ROOT}"
exec 9>"${BACKUP_LOCK_PATH}"
if ! flock -n 9; then
  echo "another SCC backup is already running" >&2
  exit 1
fi
if [[ -e "${completed_dir}" ]]; then
  echo "backup already exists: ${completed_dir}" >&2
  exit 1
fi

staging_dir="$(mktemp -d "${BACKUP_ROOT}/.${BACKUP_ID}.partial.XXXXXX")"
chmod 0700 "${staging_dir}"
mkdir "${staging_dir}/objects"

if ! docker exec "${POSTGRES_CONTAINER}" \
  pg_dump \
  --username "${POSTGRES_USER}" \
  --dbname "${POSTGRES_DB}" \
  --format custom \
  --no-owner \
  --no-acl >"${staging_dir}/postgres.dump"; then
  echo "PostgreSQL backup failed" >&2
  exit 1
fi
if [[ ! -s "${staging_dir}/postgres.dump" ]]; then
  echo "PostgreSQL backup is empty" >&2
  exit 1
fi
if ! docker exec -i "${POSTGRES_CONTAINER}" pg_restore --list <"${staging_dir}/postgres.dump" >/dev/null; then
  echo "PostgreSQL custom-format backup failed pg_restore validation" >&2
  exit 1
fi

mc_script="${staging_dir}/.mirror-minio.sh"
cat >"${mc_script}" <<'SH'
#!/bin/sh
set -eu
minio_container="$1"
bucket="$2"
IFS= read -r access_key
IFS= read -r secret_key
mc alias set scc "http://${minio_container}:9000" "${access_key}" "${secret_key}" >/dev/null
mc mirror --overwrite "scc/${bucket}" /backup/objects >/dev/null
SH
chmod 0700 "${mc_script}"
if ! printf '%s\n%s\n' "${MINIO_ACCESS_KEY}" "${MINIO_SECRET_KEY}" | docker run --rm -i \
  --user "$(id -u):$(id -g)" \
  --network "${DOCKER_NETWORK}" \
  -e MC_CONFIG_DIR=/tmp/mc \
  -v "${staging_dir}/objects:/backup/objects" \
  -v "${mc_script}:/tmp/mirror-minio.sh:ro" \
  --entrypoint /bin/sh \
  "${MINIO_MC_IMAGE}" \
  /tmp/mirror-minio.sh "${MINIO_CONTAINER}" "${MINIO_BUCKET}"; then
  echo "authenticated MinIO mirror failed" >&2
  exit 1
fi
rm -f -- "${mc_script}"

object_count="$(write_object_inventory)"
created_utc="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
metadata_file="${staging_dir}/metadata.env"
{
  printf 'format_version=1\n'
  printf 'backup_id=%s\n' "${BACKUP_ID}"
  printf 'created_utc=%s\n' "${created_utc}"
  printf 'postgres_container=%s\n' "${POSTGRES_CONTAINER}"
  printf 'postgres_database=%s\n' "${POSTGRES_DB}"
  printf 'minio_container=%s\n' "${MINIO_CONTAINER}"
  printf 'minio_bucket=%s\n' "${MINIO_BUCKET}"
  printf 'object_count=%s\n' "${object_count}"
  printf 'storage_scope=local-only\n'
} >"${metadata_file}"
chmod 0600 "${metadata_file}"

write_sha256_manifest
manifest_sha256="$(sha256_file "${staging_dir}/SHA256SUMS.jsonl")"
{
  printf 'format_version=1\n'
  printf 'completed_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'manifest_sha256=%s\n' "${manifest_sha256}"
} >"${staging_dir}/.complete"
chmod 0600 "${staging_dir}/.complete"

mv -- "${staging_dir}" "${completed_dir}"
staging_dir=""

mark_offsite_failure() {
  local reason="$1"
  {
    printf 'failed_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'reason=%s\n' "${reason}"
    printf 'local_complete_only=true\n'
  } >"${completed_dir}/.offsite-failed"
  chmod 0600 "${completed_dir}/.offsite-failed"
}

if [[ -n "${OFFSITE_HOOK}" ]]; then
  if [[ "${OFFSITE_HOOK}" != /* || ! -x "${OFFSITE_HOOK}" ]]; then
    echo "OFFSITE_HOOK must be an absolute executable path" >&2
    mark_offsite_failure invalid-hook
    if [[ "${OFFSITE_REQUIRED}" == "true" ]]; then
      echo "local .complete exists but does not satisfy the required offsite copy" >&2
      exit 1
    fi
  elif "${OFFSITE_HOOK}" "${completed_dir}"; then
    printf 'completed_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"${completed_dir}/.offsite-complete"
    chmod 0600 "${completed_dir}/.offsite-complete"
  else
    echo "WARNING: offsite hook failed; local completed backup was retained" >&2
    mark_offsite_failure hook-failed
    if [[ "${OFFSITE_REQUIRED}" == "true" ]]; then
      echo "local .complete exists but does not satisfy the required offsite copy" >&2
      exit 1
    fi
  fi
fi

apply_retention
echo "backup complete: ${completed_dir}"
echo "scope: local PostgreSQL dump and authenticated MinIO mirror"
