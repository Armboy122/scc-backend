#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

bin_dir="${tmpdir}/bin"
rootfs="${tmpdir}/rootfs"
docker_log="${tmpdir}/docker.log"
curl_log="${tmpdir}/curl.log"
backup_log="${tmpdir}/backup.log"
sudo_log="${tmpdir}/sudo.log"
command_marker="${tmpdir}/env-command-ran"
mkdir -p "${bin_dir}" "${rootfs}/opt/scc-backend/deploy/releases/20260710T010203Z" "${rootfs}/opt/scc-backend/backups" "${rootfs}/proc"
: >"${docker_log}"
: >"${curl_log}"
: >"${backup_log}"
: >"${sudo_log}"

backend_image_id='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
backend_image_ref='ghcr.io/example/scc-backend@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'

cat >"${bin_dir}/docker" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
: "${FAKE_DOCKER_LOG:?}"
printf 'docker' >>"${FAKE_DOCKER_LOG}"
for arg in "$@"; do
  printf ' %q' "${arg}" >>"${FAKE_DOCKER_LOG}"
done
if [[ -n "${JWT_SECRET+x}" || -n "${POSTGRES_PASSWORD+x}" || -n "${MINIO_SECRET_KEY+x}" || -n "${MINIO_ROOT_PASSWORD+x}" ]]; then
  printf ' leaked-secret-environment' >>"${FAKE_DOCKER_LOG}"
fi
printf '\n' >>"${FAKE_DOCKER_LOG}"

backend_id='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
other_id='sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'

container_exists() {
  [[ "${FAKE_MISSING_CONTAINER:-}" != "$1" ]]
}

case "${1:-}" in
  info)
    [[ "${FAKE_DOCKER_DAEMON_DOWN:-false}" != "true" ]]
    ;;
  network)
    [[ "${2:-}" == "inspect" && "${3:-}" == "scc-net" && "${FAKE_MISSING_NETWORK:-false}" != "true" ]]
    ;;
  volume)
    [[ "${2:-}" == "inspect" ]]
    [[ "${FAKE_MISSING_VOLUME:-}" != "${3:-}" ]]
    ;;
  image)
    [[ "${2:-}" == "inspect" && "${3:-}" == "-f" ]]
    printf '%s\n' "${other_id}"
    ;;
  inspect)
    if [[ "${2:-}" == "-f" ]]; then
      template="${3:-}"
      name="${4:-}"
    else
      template=''
      name="${2:-}"
    fi
    container_exists "${name}" || exit 1
    case "${template}" in
      *State.Running*)
        health=healthy
        image_id="${other_id}"
        image_ref="pinned@example"
        if [[ "${name}" == "scc-backend" ]]; then
          image_id="${backend_id}"
          image_ref="${backend_id}"
        fi
        [[ "${FAKE_IMAGE_DRIFT_CONTAINER:-}" == "${name}" ]] && image_id='sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
        [[ "${FAKE_UNHEALTHY_CONTAINER:-}" == "${name}" ]] && health=unhealthy
        [[ "${FAKE_MISSING_HEALTHCHECK_CONTAINER:-}" == "${name}" ]] && health=none
        running=true
        [[ "${FAKE_STOPPED_CONTAINER:-}" == "${name}" ]] && running=false
        printf '%s|%s|%s|%s|%s\n' "${running}" "${FAKE_RESTART_COUNT:-0}" "${health}" "${image_ref}" "${image_id}"
        ;;
      *Config.Healthcheck*)
        if [[ "${FAKE_MISSING_HEALTHCHECK_CONTAINER:-}" == "${name}" ]]; then
          printf 'none||\n'
          exit 0
        fi
        case "${name}" in
          scc-backend)
            command='wget -q -T 5 -O /dev/null "http://127.0.0.1:${PORT}/api/v1/readyz"'
            revision=backend-readyz-v1
            ;;
          scc-postgres)
            command='pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
            revision=postgres-pg-isready-v1
            ;;
          scc-minio)
            command='curl --fail --silent --show-error --max-time 5 http://127.0.0.1:9000/minio/health/ready >/dev/null'
            revision=minio-ready-v1
            ;;
          scc-caddy)
            command='curl --fail --silent --show-error --max-time 5 http://127.0.0.1:2019/config/ >/dev/null'
            revision=caddy-admin-v1
            ;;
        esac
        if [[ "${FAKE_HEALTHCHECK_DRIFT_CONTAINER:-}" == "${name}" ]]; then
          command=true
          revision=drifted-v0
        fi
        printf 'CMD-SHELL|%s|%s\n' "${command}" "${revision}"
        ;;
      *'.Config.Env'*)
        printf 'ENV=production\n'
        printf 'AUTO_MIGRATE=false\n'
        printf 'SEED_DATA=false\n'
        printf 'ENABLE_PHASE2_BORROWING=%s\n' "${FAKE_RUNTIME_PHASE2:-false}"
        printf 'RUN_BACKGROUND_JOBS=%s\n' "${FAKE_RUNTIME_BACKGROUND_JOBS:-true}"
        ;;
      *NetworkSettings.Networks*)
        [[ "${FAKE_WRONG_NETWORK_CONTAINER:-}" == "${name}" ]] || printf 'scc-net\n'
        ;;
      *NetworkSettings.Ports*)
        case "${name}" in
          scc-backend)
            if [[ "${FAKE_PUBLIC_BACKEND_BIND:-false}" == "true" ]]; then
              printf '8080/tcp|0.0.0.0|8080\n'
            else
              printf '8080/tcp|127.0.0.1|8080\n'
            fi
            ;;
          scc-postgres)
            ;;
          scc-minio)
            printf '9000/tcp|127.0.0.1|9000\n9001/tcp|127.0.0.1|9001\n'
            ;;
          scc-caddy)
            printf '80/tcp|0.0.0.0|80\n443/tcp|0.0.0.0|443\n'
            ;;
        esac
        ;;
      *'.Mounts'*)
        case "${name}" in
          scc-postgres)
            volume_writable=true
            [[ "${FAKE_READONLY_VOLUME_CONTAINER:-}" == "${name}" ]] && volume_writable=false
            printf 'volume|scc-postgres-data|/var/lib/docker/volumes/scc-postgres-data/_data|/var/lib/postgresql/data|%s\n' "${volume_writable}"
            ;;
          scc-minio)
            volume_writable=true
            [[ "${FAKE_READONLY_VOLUME_CONTAINER:-}" == "${name}" ]] && volume_writable=false
            printf 'volume|scc-minio-data|/var/lib/docker/volumes/scc-minio-data/_data|/data|%s\n' "${volume_writable}"
            ;;
          scc-caddy)
            printf 'volume|scc-caddy-data|/var/lib/docker/volumes/scc-caddy-data/_data|/data|true\n'
            printf 'volume|scc-caddy-config|/var/lib/docker/volumes/scc-caddy-config/_data|/config|true\n'
            printf 'bind||%s|/etc/caddy/Caddyfile|false\n' "${CADDYFILE_PATH:-/opt/scc-backend/Caddyfile}"
            ;;
        esac
        ;;
      '')
        printf '{}\n'
        ;;
      *)
        echo "unsupported docker inspect template" >&2
        exit 91
        ;;
    esac
    ;;
  *)
    echo "mutating or unsupported Docker command: ${1:-empty}" >&2
    exit 92
    ;;
esac
SH
chmod +x "${bin_dir}/docker"

cat >"${bin_dir}/df" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n'
printf '/dev/fake 1000000 %s %s %s%% /\n' "${FAKE_DISK_USED_BLOCKS:-250000}" "${FAKE_DISK_AVAILABLE_BLOCKS:-750000}" "${FAKE_DISK_USE_PERCENT:-25}"
SH
chmod +x "${bin_dir}/df"

cat >"${bin_dir}/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
: "${FAKE_CURL_LOG:?}"
printf 'curl' >>"${FAKE_CURL_LOG}"
for arg in "$@"; do printf ' %q' "${arg}" >>"${FAKE_CURL_LOG}"; done
printf '\n' >>"${FAKE_CURL_LOG}"
[[ "${FAKE_CURL_FAIL:-false}" != "true" ]]
origin=''
previous=''
is_preflight=false
for arg in "$@"; do
  if [[ "${previous}" == '--header' && "${arg}" == 'Origin: '* ]]; then
    origin="${arg#Origin: }"
  fi
  if [[ "${previous}" == '--request' && "${arg}" == 'OPTIONS' ]]; then
    is_preflight=true
  fi
  previous="${arg}"
done
if [[ "${is_preflight}" == 'true' ]]; then
  printf 'HTTP/2 204\r\n'
  case "${FAKE_MINIO_CORS_MODE:-exact}" in
    wildcard)
      printf 'Access-Control-Allow-Origin: *\r\n'
      ;;
    drift)
      [[ "${origin}" == 'https://scc.example.com' ]] && printf 'Access-Control-Allow-Origin: %s\r\n' "${origin}"
      ;;
    exact)
      case "${origin}" in
        https://scc.example.com|https://preview.scc.example.com:443)
          printf 'Access-Control-Allow-Origin: %s\r\n' "${origin}"
          ;;
      esac
      ;;
  esac
  printf '\r\nSCC_HTTP_STATUS=204\n'
fi
SH
chmod +x "${bin_dir}/curl"

cat >"${bin_dir}/sudo" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
: "${FAKE_SUDO_LOG:?}"
printf 'sudo' >>"${FAKE_SUDO_LOG}"
for arg in "$@"; do printf ' %q' "${arg}" >>"${FAKE_SUDO_LOG}"; done
printf '\n' >>"${FAKE_SUDO_LOG}"
exit 93
SH
chmod +x "${bin_dir}/sudo"

verify_backup="${tmpdir}/verify-backup.sh"
cat >"${verify_backup}" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
: "${FAKE_BACKUP_LOG:?}"
printf 'verify' >>"${FAKE_BACKUP_LOG}"
for arg in "$@"; do printf ' %q' "${arg}" >>"${FAKE_BACKUP_LOG}"; done
printf '\n' >>"${FAKE_BACKUP_LOG}"
[[ "${FAKE_BACKUP_STALE:-false}" != "true" ]]
SH
chmod +x "${verify_backup}"

app_env="${rootfs}/opt/scc-backend/.env"
cat >"${app_env}" <<'ENV'
ENV=production
AUTO_MIGRATE=false
SEED_DATA=false
ENABLE_PHASE2_BORROWING=false
RUN_BACKGROUND_JOBS=true
PORT=8080
CORS_ORIGINS=https://scc.example.com,https://preview.scc.example.com:443
JWT_SECRET=jwt-super-secret-that-must-not-leak
JWT_ACCESS_TTL=15m
JWT_REFRESH_TTL=168h
DATABASE_URL=postgresql://smartcover:postgres-super-secret@scc-postgres:5432/smartcover?sslmode=disable
POSTGRES_DB=smartcover
POSTGRES_USER=smartcover
POSTGRES_PASSWORD=postgres-super-secret
POSTGRES_IMAGE=postgres@sha256:1111111111111111111111111111111111111111111111111111111111111111
MINIO_ROOT_USER=minio-root
MINIO_ROOT_PASSWORD=minio-root-super-secret
MINIO_ACCESS_KEY=scc-application
MINIO_SECRET_KEY=minio-app-super-secret
MINIO_BUCKET=scc
MINIO_INTERNAL_ENDPOINT=scc-minio:9000
MINIO_PUBLIC_ENDPOINT=storage.scc.example.com
MINIO_INTERNAL_USE_SSL=false
MINIO_PUBLIC_USE_SSL=true
MINIO_IMAGE=minio/minio@sha256:2222222222222222222222222222222222222222222222222222222222222222
MINIO_MC_IMAGE=minio/mc@sha256:3333333333333333333333333333333333333333333333333333333333333333
API_HOST=api.scc.example.com
STORAGE_HOST=storage.scc.example.com
CADDY_IMAGE=caddy@sha256:4444444444444444444444444444444444444444444444444444444444444444
ENV
chmod 0600 "${app_env}"

printf 'persistent caddy config\n' >"${rootfs}/opt/scc-backend/Caddyfile"
cat >"${rootfs}/proc/meminfo" <<'MEMINFO'
MemTotal:        2097152 kB
MemFree:          262144 kB
MemAvailable:    1048576 kB
Buffers:           65536 kB
Cached:           524288 kB
SwapCached:            0 kB
SwapTotal:       1048576 kB
SwapFree:        1048576 kB
MEMINFO

release_dir="${rootfs}/opt/scc-backend/deploy/releases/20260710T010203Z"
cat >"${release_dir}/release.env" <<ENV
release_id=20260710T010203Z
source_commit=0123456789abcdef0123456789abcdef01234567
started_utc=2026-07-10T01:02:03Z
target_image_ref=${backend_image_ref}
target_image_id=${backend_image_id}
ENV
cat >"${release_dir}/result.env" <<'ENV'
result=success
exit_code=0
finished_utc=2026-07-10T01:03:03Z
ENV
printf '/opt/scc-backend/deploy/releases/20260710T010203Z\n' >"${rootfs}/opt/scc-backend/deploy/releases/current"

run_doctor() {
  env -i \
    PATH="${bin_dir}:${PATH}" \
    FAKE_DOCKER_LOG="${docker_log}" \
    FAKE_CURL_LOG="${curl_log}" \
    FAKE_BACKUP_LOG="${backup_log}" \
    FAKE_SUDO_LOG="${sudo_log}" \
    DOCTOR_TEST_MARKER="${command_marker}" \
    DOCTOR_FS_ROOT="${rootfs}" \
    APP_ENV_PATH=/opt/scc-backend/.env \
    CADDYFILE_PATH=/opt/scc-backend/Caddyfile \
    DOCTOR_PROC_MEMINFO=/proc/meminfo \
    DOCTOR_VERIFY_BACKUP_SCRIPT="${verify_backup}" \
    "$@" \
    "${script_dir}/doctor-vps.sh"
}

expect_success() {
  local label="$1" output
  shift
  if ! output="$(run_doctor "$@" 2>&1)"; then
    echo "FAIL: ${label} unexpectedly failed" >&2
    printf '%s\n' "${output}" >&2
    exit 1
  fi
  if [[ "${output}" != *'fail=0'* ]]; then
    echo "FAIL: ${label} did not produce a zero-failure summary" >&2
    printf '%s\n' "${output}" >&2
    exit 1
  fi
  if [[ "${output}" == *'super-secret'* || "${output}" == *'jwt-super-secret'* || "${output}" == *'must-not-leak'* ]]; then
    echo "FAIL: ${label} printed an env secret" >&2
    exit 1
  fi
  printf '%s\n' "${output}"
}

expect_failure() {
  local label="$1" expected="$2" output status
  shift 2
  set +e
  output="$(run_doctor "$@" 2>&1)"
  status=$?
  set -e
  if [[ "${status}" -eq 0 ]]; then
    echo "FAIL: ${label} unexpectedly succeeded" >&2
    printf '%s\n' "${output}" >&2
    exit 1
  fi
  if [[ "${output}" != *"${expected}"* ]]; then
    echo "FAIL: ${label} did not report expected evidence: ${expected}" >&2
    printf '%s\n' "${output}" >&2
    exit 1
  fi
  if [[ "${output}" == *'super-secret'* || "${output}" == *'jwt-super-secret'* || "${output}" == *'must-not-leak'* ]]; then
    echo "FAIL: ${label} printed an env secret" >&2
    exit 1
  fi
}

healthy_output="$(expect_success healthy)"
[[ "${healthy_output}" == *'WARN public'* ]] || { echo 'FAIL: disabled public checks were not reported' >&2; exit 1; }
[[ "${healthy_output}" == *'Phase 2 borrowing flag is explicitly false'* ]] || { echo 'FAIL: Phase 2 feature flag state was not reported' >&2; exit 1; }
if [[ "$(printf '%s\n' "${healthy_output}" | grep -c 'Docker healthcheck matches the managed contract')" -ne 4 ]]; then
  echo 'FAIL: healthy doctor run did not verify all four Docker healthcheck contracts' >&2
  exit 1
fi
[[ "${healthy_output}" != *'without its required Docker healthcheck'* ]] || { echo 'FAIL: healthy doctor run reported a missing healthcheck' >&2; exit 1; }
[[ ! -s "${curl_log}" ]] || { echo 'FAIL: healthy default run made an unconfigured public request' >&2; exit 1; }
if grep -q 'leaked-secret-environment' "${docker_log}"; then
  echo 'FAIL: application secrets remained exported to Docker inspections' >&2
  exit 1
fi
if grep -E -q '^docker (run|rm|stop|start|restart|rename|pull|login|create|prune)( |$)|^docker (network|volume) (create|rm|prune)( |$)' "${docker_log}"; then
  echo 'FAIL: doctor issued a mutating Docker command' >&2
  exit 1
fi
if [[ -s "${sudo_log}" ]]; then
  echo 'FAIL: doctor invoked sudo' >&2
  exit 1
fi

malicious_env_logical=/opt/scc-backend/malicious.env
malicious_env="${rootfs}${malicious_env_logical}"
cp "${app_env}" "${malicious_env}"
printf 'UNUSED_COMMAND=$(touch${IFS}${DOCTOR_TEST_MARKER})\n' >>"${malicious_env}"
chmod 0600 "${malicious_env}"
expect_failure 'executable env syntax' 'env file is missing, insecure, or malformed' APP_ENV_PATH="${malicious_env_logical}"
[[ ! -e "${command_marker}" ]] || { echo 'FAIL: env parser executed an env-file command substitution' >&2; exit 1; }

missing_env_logical=/opt/scc-backend/missing.env
missing_env="${rootfs}${missing_env_logical}"
awk '!/^JWT_SECRET=/' "${app_env}" >"${missing_env}"
chmod 0600 "${missing_env}"
: >"${docker_log}"
expect_failure 'missing env key' 'missing required keys: JWT_SECRET' APP_ENV_PATH="${missing_env_logical}" JWT_SECRET=inherited-secret-that-must-not-leak
if grep -q 'leaked-secret-environment' "${docker_log}"; then
  echo 'FAIL: an inherited application secret leaked to Docker when the env file omitted that key' >&2
  exit 1
fi

missing_runtime_flag_logical=/opt/scc-backend/missing-runtime-flag.env
missing_runtime_flag="${rootfs}${missing_runtime_flag_logical}"
awk '!/^RUN_BACKGROUND_JOBS=/' "${app_env}" >"${missing_runtime_flag}"
chmod 0600 "${missing_runtime_flag}"
expect_failure 'missing runtime flag' 'missing required keys: RUN_BACKGROUND_JOBS' APP_ENV_PATH="${missing_runtime_flag_logical}"

invalid_phase2_flag_logical=/opt/scc-backend/invalid-phase2-flag.env
invalid_phase2_flag="${rootfs}${invalid_phase2_flag_logical}"
sed 's/^ENABLE_PHASE2_BORROWING=false$/ENABLE_PHASE2_BORROWING=TRUE/' "${app_env}" >"${invalid_phase2_flag}"
chmod 0600 "${invalid_phase2_flag}"
expect_failure 'invalid Phase 2 flag' 'feature and background-job flags must be exactly true or false' APP_ENV_PATH="${invalid_phase2_flag_logical}"

disabled_jobs_logical=/opt/scc-backend/disabled-jobs.env
disabled_jobs="${rootfs}${disabled_jobs_logical}"
sed 's/^RUN_BACKGROUND_JOBS=true$/RUN_BACKGROUND_JOBS=false/' "${app_env}" >"${disabled_jobs}"
chmod 0600 "${disabled_jobs}"
expect_failure 'disabled active jobs' 'active production backend must own background jobs' APP_ENV_PATH="${disabled_jobs_logical}"

mutable_image_logical=/opt/scc-backend/mutable-image.env
mutable_image="${rootfs}${mutable_image_logical}"
sed 's#^CADDY_IMAGE=.*#CADDY_IMAGE=caddy:2-alpine#' "${app_env}" >"${mutable_image}"
chmod 0600 "${mutable_image}"
expect_failure 'mutable infrastructure image' 'infrastructure images must use immutable sha256 digest references' APP_ENV_PATH="${mutable_image_logical}"

expect_failure 'runtime feature drift' 'active backend flags do not match the protected production environment' FAKE_RUNTIME_PHASE2=true
expect_failure 'runtime job-owner drift' 'active backend flags do not match the protected production environment' FAKE_RUNTIME_BACKGROUND_JOBS=false
expect_failure 'infrastructure image drift' 'container scc-minio does not run its configured immutable image' FAKE_IMAGE_DRIFT_CONTAINER=scc-minio
expect_failure 'read-only PostgreSQL volume' 'container scc-postgres is missing its expected writable named volume mount' FAKE_READONLY_VOLUME_CONTAINER=scc-postgres
expect_failure 'missing Docker healthcheck' 'container scc-minio is running without its required Docker healthcheck' FAKE_MISSING_HEALTHCHECK_CONTAINER=scc-minio
expect_failure 'drifted Docker healthcheck' 'container scc-caddy Docker healthcheck command or revision has drifted' FAKE_HEALTHCHECK_DRIFT_CONTAINER=scc-caddy

expect_failure 'temporary Caddyfile' 'path must be absolute and outside temporary/runtime filesystems' CADDYFILE_PATH=/tmp/Caddyfile
expect_failure 'public backend bind' 'backend must publish 8080 only on loopback' FAKE_PUBLIC_BACKEND_BIND=true
expect_failure 'stale backup' 'latest completed backup is missing, stale, or invalid' FAKE_BACKUP_STALE=true
expect_failure 'missing Docker' 'Docker command is unavailable' DOCTOR_DOCKER_COMMAND=docker-not-installed

malformed_cors_logical=/opt/scc-backend/malformed-cors.env
malformed_cors="${rootfs}${malformed_cors_logical}"
sed 's#^CORS_ORIGINS=.*#CORS_ORIGINS=http://scc.example.com#' "${app_env}" >"${malformed_cors}"
chmod 0600 "${malformed_cors}"
expect_failure 'HTTP CORS origin' 'CORS_ORIGINS is unsafe or malformed for production' APP_ENV_PATH="${malformed_cors_logical}"

bad_minio_logical=/opt/scc-backend/bad-minio.env
bad_minio="${rootfs}${bad_minio_logical}"
sed 's/^MINIO_PUBLIC_ENDPOINT=.*/MINIO_PUBLIC_ENDPOINT=storage.other.example.com/' "${app_env}" >"${bad_minio}"
chmod 0600 "${bad_minio}"
expect_failure 'mismatched MinIO public endpoint' 'MinIO internal/public endpoint policy is invalid' APP_ENV_PATH="${bad_minio_logical}"

root_credentials_logical=/opt/scc-backend/root-credentials.env
root_credentials="${rootfs}${root_credentials_logical}"
sed \
  -e 's/^MINIO_ACCESS_KEY=.*/MINIO_ACCESS_KEY=minio-root/' \
  -e 's/^MINIO_SECRET_KEY=.*/MINIO_SECRET_KEY=minio-root-super-secret/' \
  "${app_env}" >"${root_credentials}"
chmod 0600 "${root_credentials}"
root_credentials_output="$(expect_success 'shared MinIO root credentials warning' APP_ENV_PATH="${root_credentials_logical}")"
if [[ "${root_credentials_output}" != *'WARN env'*'application MinIO credentials are the root credential pair'* ]]; then
  echo 'FAIL: shared MinIO root credentials did not produce a warning' >&2
  exit 1
fi

: >"${curl_log}"
expect_success 'explicit public TLS checks' \
  DOCTOR_ENABLE_PUBLIC_CHECKS=true \
  PUBLIC_API_HEALTHCHECK_URL=https://api.scc.example.com/api/v1/readyz \
  PUBLIC_STORAGE_HEALTHCHECK_URL=https://storage.scc.example.com/minio/health/live >/dev/null
if [[ "$(wc -l <"${curl_log}" | tr -d ' ')" != "5" ]]; then
  echo 'FAIL: explicit public mode did not make the API/storage and three CORS checks' >&2
  exit 1
fi
if grep -q -- '-k' "${curl_log}"; then
  echo 'FAIL: TLS checks disabled certificate verification' >&2
  exit 1
fi

expect_failure 'wildcard MinIO CORS' 'MinIO CORS is wildcarded or drifted for a configured origin' \
  DOCTOR_ENABLE_PUBLIC_CHECKS=true \
  PUBLIC_API_HEALTHCHECK_URL=https://api.scc.example.com/api/v1/readyz \
  PUBLIC_STORAGE_HEALTHCHECK_URL=https://storage.scc.example.com/minio/health/live \
  FAKE_MINIO_CORS_MODE=wildcard

expect_failure 'configured-origin MinIO CORS drift' 'MinIO CORS is wildcarded or drifted for a configured origin' \
  DOCTOR_ENABLE_PUBLIC_CHECKS=true \
  PUBLIC_API_HEALTHCHECK_URL=https://api.scc.example.com/api/v1/readyz \
  PUBLIC_STORAGE_HEALTHCHECK_URL=https://storage.scc.example.com/minio/health/live \
  FAKE_MINIO_CORS_MODE=drift

if grep -E -q '^docker (run|rm|stop|start|restart|rename|pull|login|create|prune)( |$)|^docker (network|volume) (create|rm|prune)( |$)' "${docker_log}"; then
  echo 'FAIL: a failure-mode doctor run issued a mutating Docker command' >&2
  exit 1
fi
if [[ -s "${sudo_log}" ]]; then
  echo 'FAIL: a failure-mode doctor run invoked sudo' >&2
  exit 1
fi

bash -n "${script_dir}/doctor-vps.sh" "${script_dir}/test-doctor-vps.sh"
echo 'doctor-vps tests passed'
