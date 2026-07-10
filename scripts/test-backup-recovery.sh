#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

bin_dir="${tmpdir}/bin"
docker_state="${tmpdir}/docker-state"
docker_log="${tmpdir}/docker.log"
mkdir -p "${bin_dir}" "${docker_state}/containers" "${docker_state}/networks" "${docker_state}/volumes"
: >"${docker_log}"

export POSTGRES_IMAGE=postgres@sha256:1111111111111111111111111111111111111111111111111111111111111111
export MINIO_IMAGE=minio/minio@sha256:2222222222222222222222222222222222222222222222222222222222222222
export MINIO_MC_IMAGE=minio/mc@sha256:3333333333333333333333333333333333333333333333333333333333333333

cat >"${bin_dir}/docker" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
: "${FAKE_DOCKER_LOG:?}"
: "${FAKE_DOCKER_STATE:?}"
printf 'docker' >>"${FAKE_DOCKER_LOG}"
if [[ -n "${POSTGRES_PASSWORD+x}" || -n "${MINIO_ACCESS_KEY+x}" || -n "${MINIO_SECRET_KEY+x}" ]]; then
  printf ' leaked-secret-environment' >>"${FAKE_DOCKER_LOG}"
fi
for arg in "$@"; do
  printf ' %q' "${arg}" >>"${FAKE_DOCKER_LOG}"
done
printf '\n' >>"${FAKE_DOCKER_LOG}"

has_arg() {
  local expected="$1" arg
  shift
  for arg in "$@"; do
    [[ "${arg}" == "${expected}" ]] && return 0
  done
  return 1
}

case "${1:-}" in
  exec)
    if has_arg pg_dump "$@"; then
      [[ "${FAIL_PG_DUMP:-false}" != "true" ]] || exit 70
      printf 'fake-postgresql-custom-format-dump'
      exit 0
    fi
    if has_arg pg_restore "$@" && has_arg --list "$@"; then
      [[ "${FAIL_PG_RESTORE_LIST:-false}" != "true" ]] || exit 71
      exit 0
    fi
    [[ "${FAIL_SCRATCH_DB_RESTORE:-false}" != "true" ]] || {
      has_arg pg_restore "$@" && exit 72
    }
    exit 0
    ;;
  run)
    if has_arg -i "$@"; then
      IFS= read -r _credential_one || true
      IFS= read -r _credential_two || true
    fi
    if has_arg /tmp/mirror-minio.sh "$@"; then
      [[ "${FAIL_MINIO_MIRROR:-false}" != "true" ]] || exit 73
      for arg in "$@"; do
        case "${arg}" in
          *:/backup/objects)
            object_dir="${arg%:/backup/objects}"
            mkdir -p "${object_dir}/nested"
            printf 'photo-content' >"${object_dir}/photo.jpg"
            printf 'document-content' >"${object_dir}/nested/document.pdf"
            ;;
        esac
      done
      exit 0
    fi
    if has_arg /tmp/restore-minio.sh "$@"; then
      [[ "${FAIL_SCRATCH_MINIO_RESTORE:-false}" != "true" ]] || exit 74
      exit 0
    fi
    name=''
    previous=''
    for arg in "$@"; do
      if [[ "${previous}" == "--name" ]]; then
        name="${arg}"
        break
      fi
      previous="${arg}"
    done
    if [[ -n "${name}" ]]; then
      : >"${FAKE_DOCKER_STATE}/containers/${name}"
    fi
    exit 0
    ;;
  inspect)
    [[ -e "${FAKE_DOCKER_STATE}/containers/${2:-missing}" ]]
    ;;
  rm)
    rm -f "${FAKE_DOCKER_STATE}/containers/${@: -1}"
    ;;
  network)
    action="${2:-}"
    name="${3:-}"
    case "${action}" in
      inspect) [[ -e "${FAKE_DOCKER_STATE}/networks/${name}" ]] ;;
      create) : >"${FAKE_DOCKER_STATE}/networks/${name}" ;;
      rm) rm -f "${FAKE_DOCKER_STATE}/networks/${name}" ;;
      *) exit 0 ;;
    esac
    ;;
  volume)
    action="${2:-}"
    name="${3:-}"
    case "${action}" in
      inspect) [[ -e "${FAKE_DOCKER_STATE}/volumes/${name}" ]] ;;
      create) : >"${FAKE_DOCKER_STATE}/volumes/${name}" ;;
      rm) rm -f "${FAKE_DOCKER_STATE}/volumes/${name}" ;;
      *) exit 0 ;;
    esac
    ;;
  *)
    exit 0
    ;;
esac
SH
chmod +x "${bin_dir}/docker"

cat >"${bin_dir}/flock" <<'SH'
#!/usr/bin/env bash
[[ "${FAIL_FLOCK:-false}" != "true" ]]
SH
chmod +x "${bin_dir}/flock"

cat >"${bin_dir}/sleep" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "${bin_dir}/sleep"

app_env="${tmpdir}/app.env"
cat >"${app_env}" <<'ENV'
POSTGRES_DB=smartcover
POSTGRES_USER=smartcover
export POSTGRES_PASSWORD=postgres-super-secret
export MINIO_ACCESS_KEY=minio-secret-access
export MINIO_SECRET_KEY=minio-super-secret-value
MINIO_BUCKET=scc
MINIO_MC_IMAGE=minio/mc@sha256:3333333333333333333333333333333333333333333333333333333333333333
ENV
chmod 0600 "${app_env}"

run_backup() {
  local backup_root="$1" backup_id="$2"
  shift 2
  env \
    PATH="${bin_dir}:${PATH}" \
    FAKE_DOCKER_LOG="${docker_log}" \
    FAKE_DOCKER_STATE="${docker_state}" \
    APP_ENV_PATH="${app_env}" \
    BACKUP_ROOT="${backup_root}" \
    BACKUP_ID="${backup_id}" \
    "$@" \
    "${script_dir}/backup-vps.sh"
}

run_verify() {
  env \
    PATH="${bin_dir}:${PATH}" \
    FAKE_DOCKER_LOG="${docker_log}" \
    FAKE_DOCKER_STATE="${docker_state}" \
    "${script_dir}/verify-backup.sh" \
    "$@"
}

seed_complete_backup() {
  mkdir -p "$1"
  : >"$1/.complete"
}

backup_root="${tmpdir}/backups"
chmod 0644 "${app_env}"
: >"${docker_log}"
if run_backup "${tmpdir}/insecure-env-backups" 20260701T010000Z >/dev/null 2>&1; then
  echo "FAIL: group/world-readable APP_ENV_PATH was accepted" >&2
  exit 1
fi
if [[ -s "${docker_log}" ]]; then
  echo "FAIL: insecure APP_ENV_PATH was rejected only after Docker work" >&2
  exit 1
fi
chmod 0600 "${app_env}"

malicious_env="${tmpdir}/malicious-app.env"
cp "${app_env}" "${malicious_env}"
printf 'UNUSED_COMMAND=$(touch %s)\n' "${tmpdir}/backup-env-command-ran" >>"${malicious_env}"
: >"${docker_log}"
if run_backup "${tmpdir}/malicious-env-backups" 20260701T020000Z "APP_ENV_PATH=${malicious_env}" >/dev/null 2>&1; then
  echo "FAIL: executable application env syntax was accepted by backup" >&2
  exit 1
fi
if [[ -e "${tmpdir}/backup-env-command-ran" || -s "${docker_log}" ]]; then
  echo "FAIL: backup executed env syntax or reached Docker after rejecting it" >&2
  exit 1
fi

mutable_image_env="${tmpdir}/mutable-image-app.env"
sed 's#^MINIO_MC_IMAGE=.*#MINIO_MC_IMAGE=minio/mc:latest#' "${app_env}" >"${mutable_image_env}"
chmod 0600 "${mutable_image_env}"
: >"${docker_log}"
if run_backup "${tmpdir}/mutable-image-backups" 20260701T030000Z "APP_ENV_PATH=${mutable_image_env}" >/dev/null 2>&1; then
  echo "FAIL: mutable MinIO client image was accepted by backup" >&2
  exit 1
fi
if [[ -s "${docker_log}" ]]; then
  echo "FAIL: mutable backup image was rejected only after Docker work" >&2
  exit 1
fi

for backup_id in \
  20260709T010000Z 20260708T010000Z 20260707T010000Z 20260706T010000Z \
  20260705T010000Z 20260704T010000Z 20260703T010000Z 20260702T010000Z \
  20260625T010000Z 20260618T010000Z 20260611T010000Z 20260604T010000Z \
  20260515T010000Z 20260415T010000Z 20260315T010000Z 20260215T010000Z \
  20260115T010000Z 20251215T010000Z; do
  seed_complete_backup "${backup_root}/${backup_id}"
done

run_backup "${backup_root}" 20260710T010000Z >/dev/null
completed_backup="${backup_root}/20260710T010000Z"

for artifact in postgres.dump objects object-inventory.jsonl metadata.env SHA256SUMS.jsonl .complete; do
  if [[ ! -e "${completed_backup}/${artifact}" ]]; then
    echo "FAIL: completed backup is missing ${artifact}" >&2
    exit 1
  fi
done
if find "${backup_root}" -maxdepth 1 -name '.*.partial.*' -print -quit | grep -q .; then
  echo "FAIL: successful backup left a partial directory" >&2
  exit 1
fi
if grep -R -E -q 'postgres-super-secret|minio-secret-access|minio-super-secret-value' "${completed_backup}" "${docker_log}"; then
  echo "FAIL: backup artifacts or Docker arguments exposed a secret" >&2
  exit 1
fi
if grep -q 'leaked-secret-environment' "${docker_log}"; then
  echo "FAIL: sourced application secrets were exported to Docker" >&2
  exit 1
fi
if ! grep -q '^storage_scope=local-only$' "${completed_backup}/metadata.env"; then
  echo "FAIL: metadata must not claim the local snapshot is offsite" >&2
  exit 1
fi

run_verify --backup "${completed_backup}" >/dev/null
if ! grep -q 'docker run --rm -i --entrypoint pg_restore postgres@sha256:1111111111111111111111111111111111111111111111111111111111111111 --list' "${docker_log}"; then
  echo "FAIL: backup verification did not parse the custom-format PostgreSQL dump" >&2
  exit 1
fi

for kept in 20260710T010000Z 20260704T010000Z 20260625T010000Z 20260618T010000Z 20260515T010000Z 20260215T010000Z; do
  [[ -d "${backup_root}/${kept}" ]] || { echo "FAIL: retention removed required tier representative ${kept}" >&2; exit 1; }
done
for removed in 20260703T010000Z 20260702T010000Z 20260611T010000Z 20260604T010000Z 20260115T010000Z 20251215T010000Z; do
  [[ ! -e "${backup_root}/${removed}" ]] || { echo "FAIL: retention kept expired backup ${removed}" >&2; exit 1; }
done

failed_root="${tmpdir}/failed-backups"
: >"${docker_log}"
if run_backup "${failed_root}" 20260711T010000Z FAIL_MINIO_MIRROR=true >/dev/null 2>&1; then
  echo "FAIL: MinIO mirror failure must fail the backup" >&2
  exit 1
fi
if [[ -e "${failed_root}/20260711T010000Z" ]] || find "${failed_root}" -maxdepth 1 -name '.*.partial.*' -print -quit | grep -q .; then
  echo "FAIL: failed backup was published or left partial state" >&2
  exit 1
fi

lock_root="${tmpdir}/locked-backups"
: >"${docker_log}"
if run_backup "${lock_root}" 20260712T010000Z FAIL_FLOCK=true >/dev/null 2>&1; then
  echo "FAIL: backup lock contention must fail" >&2
  exit 1
fi
if [[ -s "${docker_log}" ]]; then
  echo "FAIL: backup touched Docker after lock acquisition failed" >&2
  exit 1
fi

: >"${docker_log}"
control_identifier="POSTGRES_CONTAINER=bad"$'\t'"container"
if run_backup "${tmpdir}/control-id-backups" 20260713T010000Z "${control_identifier}" >/dev/null 2>&1; then
  echo "FAIL: control character in emitted metadata identifier was accepted" >&2
  exit 1
fi
if [[ -s "${docker_log}" ]]; then
  echo "FAIL: unsafe metadata identifier was rejected only after Docker work" >&2
  exit 1
fi

offsite_hook="${tmpdir}/offsite-hook-fails"
cat >"${offsite_hook}" <<'SH'
#!/usr/bin/env bash
exit 75
SH
chmod 0700 "${offsite_hook}"
optional_offsite_root="${tmpdir}/optional-offsite"
run_backup "${optional_offsite_root}" 20260714T010000Z \
  "OFFSITE_HOOK=${offsite_hook}" OFFSITE_REQUIRED=false >/dev/null 2>&1
if [[ ! -f "${optional_offsite_root}/20260714T010000Z/.complete" || \
  ! -f "${optional_offsite_root}/20260714T010000Z/.offsite-failed" || \
  -e "${optional_offsite_root}/20260714T010000Z/.offsite-complete" ]]; then
  echo "FAIL: optional offsite failure markers are incorrect" >&2
  exit 1
fi
run_verify --backup "${optional_offsite_root}/20260714T010000Z" >/dev/null

required_offsite_root="${tmpdir}/required-offsite"
if run_backup "${required_offsite_root}" 20260715T010000Z \
  "OFFSITE_HOOK=${offsite_hook}" OFFSITE_REQUIRED=true >/dev/null 2>&1; then
  echo "FAIL: required offsite hook failure did not fail the job" >&2
  exit 1
fi
if [[ ! -f "${required_offsite_root}/20260715T010000Z/.complete" || \
  ! -f "${required_offsite_root}/20260715T010000Z/.offsite-failed" || \
  -e "${required_offsite_root}/20260715T010000Z/.offsite-complete" ]]; then
  echo "FAIL: required offsite failure was mistaken for offsite success" >&2
  exit 1
fi

case_root="${tmpdir}/verify-cases"
mkdir -p "${case_root}"
mkdir -p "${case_root}/corrupt"
cp -R "${completed_backup}" "${case_root}/corrupt/20260710T010000Z"
printf 'tampered' >>"${case_root}/corrupt/20260710T010000Z/objects/photo.jpg"
if run_verify --backup "${case_root}/corrupt/20260710T010000Z" >/dev/null 2>&1; then
  echo "FAIL: corrupt object checksum was accepted" >&2
  exit 1
fi

mkdir -p "${case_root}/missing-object"
cp -R "${completed_backup}" "${case_root}/missing-object/20260710T010000Z"
rm -f "${case_root}/missing-object/20260710T010000Z/objects/photo.jpg"
if run_verify --backup "${case_root}/missing-object/20260710T010000Z" >/dev/null 2>&1; then
  echo "FAIL: missing mirrored object was accepted" >&2
  exit 1
fi

mkdir -p "${case_root}/missing-db"
cp -R "${completed_backup}" "${case_root}/missing-db/20260710T010000Z"
rm -f "${case_root}/missing-db/20260710T010000Z/postgres.dump"
if run_verify --backup "${case_root}/missing-db/20260710T010000Z" >/dev/null 2>&1; then
  echo "FAIL: missing PostgreSQL dump was accepted" >&2
  exit 1
fi

mkdir -p "${case_root}/traversal"
cp -R "${completed_backup}" "${case_root}/traversal/20260710T010000Z"
python3 - "${case_root}/traversal/20260710T010000Z" <<'PY'
import hashlib, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
manifest = root / "SHA256SUMS.jsonl"
with manifest.open("a", encoding="utf-8") as output:
    output.write(json.dumps({
        "path": "../outside-backup",
        "sha256": hashlib.sha256(b"").hexdigest(),
        "size": 0,
    }, sort_keys=True, separators=(",", ":")) + "\n")
marker = {}
for line in (root / ".complete").read_text(encoding="utf-8").splitlines():
    key, value = line.split("=", 1)
    marker[key] = value
marker["manifest_sha256"] = hashlib.sha256(manifest.read_bytes()).hexdigest()
(root / ".complete").write_text(
    "\n".join(f"{key}={marker[key]}" for key in ["format_version", "completed_utc", "manifest_sha256"]) + "\n",
    encoding="utf-8",
)
PY
if run_verify --backup "${case_root}/traversal/20260710T010000Z" >/dev/null 2>&1; then
  echo "FAIL: parent-directory manifest traversal was accepted" >&2
  exit 1
fi

: >"${docker_log}"
if env \
  PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${docker_log}" \
  FAKE_DOCKER_STATE="${docker_state}" \
  RESTORE_STATE_DIR="${tmpdir}/corrupt-restore-state" \
  RESTORE_ID=20260710T015000Z \
  "${script_dir}/restore-vps.sh" \
  --backup "${case_root}/corrupt/20260710T010000Z" --target scratch >/dev/null 2>&1; then
  echo "FAIL: restore accepted a corrupt backup" >&2
  exit 1
fi
if grep -q -- '--name scc-restore-' "${docker_log}"; then
  echo "FAIL: restore created scratch resources before backup integrity passed" >&2
  exit 1
fi

now_id="$(date -u +%Y%m%dT%H%M%SZ)"
fresh_root="${tmpdir}/freshness"
mkdir -p "${fresh_root}"
cp -R "${completed_backup}" "${fresh_root}/${now_id}"
env \
  PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${docker_log}" \
  FAKE_DOCKER_STATE="${docker_state}" \
  BACKUP_ROOT="${fresh_root}" \
  "${script_dir}/verify-backup.sh" --latest --max-age-seconds 60 --freshness-only >/dev/null
mkdir -p "${fresh_root}/20000101T000000Z"
cp "${completed_backup}/.complete" "${completed_backup}/SHA256SUMS.jsonl" "${completed_backup}/metadata.env" \
  "${completed_backup}/object-inventory.jsonl" "${completed_backup}/postgres.dump" "${fresh_root}/20000101T000000Z/"
mkdir "${fresh_root}/20000101T000000Z/objects"
if run_verify --backup "${fresh_root}/20000101T000000Z" --max-age-seconds 60 --freshness-only >/dev/null 2>&1; then
  echo "FAIL: stale backup passed freshness gate" >&2
  exit 1
fi

: >"${docker_log}"
if env \
  PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${docker_log}" \
  FAKE_DOCKER_STATE="${docker_state}" \
  APP_ENV_PATH="${app_env}" \
  RESTORE_STATE_DIR="${tmpdir}/restore-state" \
  "${script_dir}/restore-vps.sh" --backup "${completed_backup}" --target live >/dev/null 2>&1; then
  echo "FAIL: live restore without confirmation was accepted" >&2
  exit 1
fi
if [[ -s "${docker_log}" ]]; then
  echo "FAIL: refused live restore touched Docker" >&2
  exit 1
fi
if env \
  PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${docker_log}" \
  FAKE_DOCKER_STATE="${docker_state}" \
  APP_ENV_PATH="${app_env}" \
  RESTORE_STATE_DIR="${tmpdir}/restore-state" \
  "${script_dir}/restore-vps.sh" --backup "${completed_backup}" --target live \
  --confirm-live-overwrite ERASE_LIVE_SCC_DATA >/dev/null 2>&1; then
  echo "FAIL: first restore version unexpectedly implemented live overwrite" >&2
  exit 1
fi

: >"${docker_log}"
env \
  PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${docker_log}" \
  FAKE_DOCKER_STATE="${docker_state}" \
  APP_ENV_PATH="${app_env}" \
  RESTORE_STATE_DIR="${tmpdir}/restore-state" \
  RESTORE_ID=20260710T020000Z \
  KEEP_SCRATCH=false \
  "${script_dir}/restore-vps.sh" --backup "${completed_backup}" --target scratch >/dev/null

if ! grep -q -- '--name scc-restore-postgres-20260710T020000Z' "${docker_log}" || \
  ! grep -q -- '--name scc-restore-minio-20260710T020000Z' "${docker_log}" || \
  ! grep -q 'pg_restore --username restore --dbname restore' "${docker_log}"; then
  echo "FAIL: scratch restore did not restore both PostgreSQL and MinIO targets" >&2
  exit 1
fi
if grep -E -q -- '--name scc-(postgres|minio)( |$)|rm -f scc-(postgres|minio)( |$)' "${docker_log}"; then
  echo "FAIL: scratch restore targeted a live container" >&2
  exit 1
fi
if grep -E -q 'postgres-super-secret|minio-secret-access|minio-super-secret-value' "${docker_log}"; then
  echo "FAIL: restore Docker arguments exposed an application secret" >&2
  exit 1
fi

: >"${docker_log}"
if env \
  PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${docker_log}" \
  FAKE_DOCKER_STATE="${docker_state}" \
  FAIL_FLOCK=true \
  APP_ENV_PATH="${app_env}" \
  RESTORE_STATE_DIR="${tmpdir}/restore-lock-state" \
  RESTORE_ID=20260710T030000Z \
  "${script_dir}/restore-vps.sh" --backup "${completed_backup}" >/dev/null 2>&1; then
  echo "FAIL: restore lock contention must fail" >&2
  exit 1
fi
if [[ -s "${docker_log}" ]]; then
  echo "FAIL: restore touched Docker after lock acquisition failed" >&2
  exit 1
fi

for timer in scc-backup.timer scc-backup-freshness.timer; do
  if ! grep -q '^Persistent=true$' "${script_dir}/../deploy/systemd/${timer}" || \
    ! grep -q '^RandomizedDelaySec=' "${script_dir}/../deploy/systemd/${timer}"; then
    echo "FAIL: ${timer} must be persistent and randomized" >&2
    exit 1
  fi
done
if ! grep -q -- '--max-age-seconds 93600 --freshness-only' \
  "${script_dir}/../deploy/systemd/scc-backup-freshness.service"; then
  echo "FAIL: systemd freshness service is missing its age gate" >&2
  exit 1
fi
if ! grep -q '^Environment=DOCKER_CONFIG=/tmp/scc-docker-config$' \
  "${script_dir}/../deploy/systemd/scc-backup.service" || \
  ! grep -q '^ExecStartPre=/usr/bin/mkdir -p /tmp/scc-docker-config$' \
  "${script_dir}/../deploy/systemd/scc-backup.service"; then
  echo "FAIL: systemd backup service must use a private writable Docker config" >&2
  exit 1
fi

echo "backup and recovery checks passed"
