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
: "${POSTGRES_IMAGE:=}"
: "${MINIO_CONTAINER:=scc-minio}"
: "${MINIO_VOLUME:=scc-minio-data}"
: "${MINIO_IMAGE:=}"
: "${MINIO_MC_IMAGE:=}"
: "${MINIO_API_HOST_PORT:=127.0.0.1:9000}"
: "${MINIO_CONSOLE_HOST_PORT:=127.0.0.1:9001}"
: "${MINIO_CONFIGURE_CORS:=best-effort}"
: "${CADDY_CONTAINER:=scc-caddy}"
: "${CADDY_IMAGE:=}"
: "${CADDY_DATA_VOLUME:=scc-caddy-data}"
: "${CADDY_CONFIG_VOLUME:=scc-caddy-config}"
: "${CADDYFILE_PATH:=/opt/scc-backend/Caddyfile}"
: "${MANAGE_CADDY:=true}"
: "${DISABLE_NGINX:=true}"
: "${SKIP_IMAGE_PULL:=false}"
: "${SKIP_INFRA_IMAGE_PULL:=false}"
: "${ALLOW_MUTABLE_IMAGE:=false}"
: "${CANDIDATE_CONTAINER:=${CONTAINER_NAME}-candidate}"
: "${PREVIOUS_CONTAINER:=${CONTAINER_NAME}-previous}"
: "${CANDIDATE_HOST_PORT:=127.0.0.1:18080}"
: "${CADDY_PREVIOUS_CONTAINER:=${CADDY_CONTAINER}-previous}"
: "${POSTGRES_HEALTH_PREVIOUS_CONTAINER:=${POSTGRES_CONTAINER}-healthcheck-previous}"
: "${MINIO_HEALTH_PREVIOUS_CONTAINER:=${MINIO_CONTAINER}-healthcheck-previous}"
: "${DEPLOY_STATE_DIR:=$(dirname -- "${APP_ENV_PATH}")/deploy}"
: "${DEPLOY_LOCK_PATH:=${DEPLOY_STATE_DIR}/deploy.lock}"
: "${RELEASES_DIR:=${DEPLOY_STATE_DIR}/releases}"
: "${PREDEPLOY_BACKUP_DIR:=${DEPLOY_STATE_DIR}/predeploy-backups}"
: "${PREDEPLOY_BACKUP_REQUIRED:=true}"
: "${VERIFY_CADDY_RESTART:=true}"
: "${RELEASE_ID:=$(date -u +%Y%m%dT%H%M%SZ)}"
: "${SOURCE_COMMIT:=unknown}"
: "${HEALTHCHECK_TIMEOUT_SECONDS:=10}"

HEALTHCHECK_LABEL_KEY=io.smartcover.healthcheck
BACKEND_HEALTHCHECK_REVISION=backend-readyz-v1
POSTGRES_HEALTHCHECK_REVISION=postgres-pg-isready-v1
MINIO_HEALTHCHECK_REVISION=minio-ready-v1
CADDY_HEALTHCHECK_REVISION=caddy-admin-v1
BACKEND_HEALTHCHECK_COMMAND='wget -q -T 5 -O /dev/null "http://127.0.0.1:${PORT}/api/v1/readyz"'
POSTGRES_HEALTHCHECK_COMMAND='pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
MINIO_HEALTHCHECK_COMMAND='curl --fail --silent --show-error --max-time 5 http://127.0.0.1:9000/minio/health/ready >/dev/null'
CADDY_HEALTHCHECK_COMMAND='curl --fail --silent --show-error --max-time 5 http://127.0.0.1:2019/config/ >/dev/null'

umask 077
export -n GHCR_TOKEN GHCR_USERNAME 2>/dev/null || true

is_managed_app_env_key() {
  case "$1" in
    ENV|AUTO_MIGRATE|SEED_DATA|ENABLE_PHASE2_BORROWING|RUN_BACKGROUND_JOBS|PORT|CORS_ORIGINS|JWT_SECRET|JWT_ACCESS_TTL|JWT_REFRESH_TTL|DATABASE_URL|POSTGRES_DB|POSTGRES_USER|POSTGRES_PASSWORD|POSTGRES_IMAGE|MINIO_ROOT_USER|MINIO_ROOT_PASSWORD|MINIO_ACCESS_KEY|MINIO_SECRET_KEY|MINIO_BUCKET|MINIO_INTERNAL_ENDPOINT|MINIO_PUBLIC_ENDPOINT|MINIO_INTERNAL_USE_SSL|MINIO_PUBLIC_USE_SSL|MINIO_IMAGE|MINIO_MC_IMAGE|MINIO_CONFIGURE_CORS|API_HOST|STORAGE_HOST|CADDY_EMAIL|CADDY_IMAGE)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

is_application_runtime_env_key() {
  case "$1" in
    ENV|ENABLE_PHASE2_BORROWING|PORT|CORS_ORIGINS|JWT_SECRET|JWT_ACCESS_TTL|JWT_REFRESH_TTL|DATABASE_URL|MIGRATION_TIMEOUT|MINIO_ACCESS_KEY|MINIO_SECRET_KEY|MINIO_BUCKET|MINIO_INTERNAL_ENDPOINT|MINIO_PUBLIC_ENDPOINT|MINIO_INTERNAL_USE_SSL|MINIO_PUBLIC_USE_SSL)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

clear_managed_app_env() {
  local key
  for key in \
    ENV AUTO_MIGRATE SEED_DATA ENABLE_PHASE2_BORROWING RUN_BACKGROUND_JOBS PORT \
    CORS_ORIGINS JWT_SECRET JWT_ACCESS_TTL JWT_REFRESH_TTL DATABASE_URL \
    POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD POSTGRES_IMAGE \
    MINIO_ROOT_USER MINIO_ROOT_PASSWORD MINIO_ACCESS_KEY MINIO_SECRET_KEY MINIO_BUCKET \
    MINIO_INTERNAL_ENDPOINT MINIO_PUBLIC_ENDPOINT MINIO_INTERNAL_USE_SSL MINIO_PUBLIC_USE_SSL \
    MINIO_IMAGE MINIO_MC_IMAGE MINIO_CONFIGURE_CORS API_HOST STORAGE_HOST CADDY_EMAIL CADDY_IMAGE; do
    unset "${key}"
  done
}

file_mode() {
  local path="$1" mode
  if mode="$(stat -c '%a' -- "${path}" 2>/dev/null)" && [[ "${mode}" =~ ^[0-7]{3,4}$ ]]; then
    printf '%s\n' "${mode}"
    return 0
  fi
  if mode="$(stat -f '%Lp' -- "${path}" 2>/dev/null)" && [[ "${mode}" =~ ^[0-7]{3,4}$ ]]; then
    printf '%s\n' "${mode}"
    return 0
  fi
  return 1
}

load_protected_app_env() {
  local path="${APP_ENV_PATH}" mode permission_digits group_digit other_digit
  local raw line key value quote seen_key
  local seen_keys=(__deploy_no_env_key__)
  APP_ENV_KEYS=()
  APP_ENV_VALUES=()

  if [[ "${path}" != /* || ! -f "${path}" || -L "${path}" || ! -r "${path}" ]]; then
    echo "APP_ENV_PATH must be an absolute, readable, regular non-symlink file" >&2
    return 1
  fi
  if ! mode="$(file_mode "${path}")"; then
    echo "APP_ENV_PATH permissions could not be inspected" >&2
    return 1
  fi
  permission_digits="${mode: -3}"
  group_digit="${permission_digits:1:1}"
  other_digit="${permission_digits:2:1}"
  if [[ "${group_digit}" != "0" || "${other_digit}" != "0" ]]; then
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
    APP_ENV_KEYS+=("${key}")
    APP_ENV_VALUES+=("${value}")
    if is_managed_app_env_key "${key}"; then
      printf -v "${key}" '%s' "${value}"
      export -n "${key}" 2>/dev/null || true
    fi
  done <"${path}"
}

clear_managed_app_env
if ! load_protected_app_env; then
  echo "protected application env parsing failed: ${APP_ENV_PATH}" >&2
  exit 1
fi
: "${MINIO_CONFIGURE_CORS:=best-effort}"

required_env=(
  ENV
  AUTO_MIGRATE
  SEED_DATA
  ENABLE_PHASE2_BORROWING
  RUN_BACKGROUND_JOBS
  DATABASE_URL
  JWT_SECRET
  POSTGRES_DB
  POSTGRES_USER
  POSTGRES_PASSWORD
  POSTGRES_IMAGE
  MINIO_ROOT_USER
  MINIO_ROOT_PASSWORD
  MINIO_BUCKET
  MINIO_INTERNAL_ENDPOINT
  MINIO_PUBLIC_ENDPOINT
  MINIO_INTERNAL_USE_SSL
  MINIO_PUBLIC_USE_SSL
  MINIO_ACCESS_KEY
  MINIO_SECRET_KEY
  MINIO_IMAGE
  MINIO_MC_IMAGE
  CORS_ORIGINS
  CADDY_IMAGE
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

if [[ "${ENV}" != "production" ]]; then
  echo "ENV=production is required for the VPS deploy" >&2
  exit 1
fi
if [[ "${AUTO_MIGRATE}" != "false" || "${SEED_DATA}" != "false" ]]; then
  echo "production deploy requires AUTO_MIGRATE=false and SEED_DATA=false" >&2
  exit 1
fi
if [[ "${ENABLE_PHASE2_BORROWING}" != "true" && "${ENABLE_PHASE2_BORROWING}" != "false" ]]; then
  echo "ENABLE_PHASE2_BORROWING must be exactly true or false" >&2
  exit 1
fi
if [[ "${RUN_BACKGROUND_JOBS}" != "true" ]]; then
  echo "RUN_BACKGROUND_JOBS must be true in the active production env; deploy candidates override it to false" >&2
  exit 1
fi
if [[ "${PREDEPLOY_BACKUP_REQUIRED}" != "true" ]]; then
  echo "production deploy requires PREDEPLOY_BACKUP_REQUIRED=true before forward migrations" >&2
  exit 1
fi

if [[ "${MINIO_INTERNAL_ENDPOINT}" != "${MINIO_CONTAINER}:9000" || "${MINIO_INTERNAL_USE_SSL}" != "false" ]]; then
  echo "MINIO_INTERNAL_ENDPOINT must be ${MINIO_CONTAINER}:9000 with MINIO_INTERNAL_USE_SSL=false" >&2
  exit 1
fi
if [[ "${MINIO_PUBLIC_USE_SSL}" != "true" ]]; then
  echo "MINIO_PUBLIC_USE_SSL must be true for production browser signing" >&2
  exit 1
fi
if [[ "${MINIO_PUBLIC_ENDPOINT}" == "${MINIO_INTERNAL_ENDPOINT}" ]]; then
  echo "MINIO_PUBLIC_ENDPOINT must be distinct from the internal Docker endpoint" >&2
  exit 1
fi
case "${MINIO_CONFIGURE_CORS}" in
  best-effort|required|false|off|skip|true|on)
    ;;
  *)
    echo "MINIO_CONFIGURE_CORS must be best-effort, required, false, off, skip, true, or on" >&2
    exit 1
    ;;
esac
IFS=',' read -r -a configured_cors_origins <<<"${CORS_ORIGINS}"
for origin in "${configured_cors_origins[@]}"; do
  if [[ ! "${origin}" =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?$ ]]; then
    echo "CORS_ORIGINS must contain explicit HTTPS production origins; wildcards are forbidden" >&2
    exit 1
  fi
done

if [[ "${MANAGE_CADDY}" == "true" ]]; then
  if [[ ! "${API_HOST}" =~ ^[A-Za-z0-9.-]+$ || ! "${STORAGE_HOST}" =~ ^[A-Za-z0-9.-]+$ ]]; then
    echo "API_HOST and STORAGE_HOST may contain only letters, digits, dot, and dash" >&2
    exit 1
  fi
  if [[ ! "${CADDY_EMAIL:-admin@${API_HOST}}" =~ ^[^[:space:]{}]+@[^[:space:]{}]+$ ]]; then
    echo "CADDY_EMAIL contains unsupported characters" >&2
    exit 1
  fi
  if [[ "${MINIO_PUBLIC_ENDPOINT}" != "${STORAGE_HOST}" ]]; then
    echo "MINIO_PUBLIC_ENDPOINT must match STORAGE_HOST when Caddy is managed" >&2
    exit 1
  fi
fi

if [[ -z "${PUBLIC_API_HEALTHCHECK_URL:-}" && -n "${API_HOST:-}" ]]; then
  PUBLIC_API_HEALTHCHECK_URL="https://${API_HOST}/api/v1/health"
fi
if [[ -z "${PUBLIC_STORAGE_HEALTHCHECK_URL:-}" && -n "${STORAGE_HOST:-}" ]]; then
  PUBLIC_STORAGE_HEALTHCHECK_URL="https://${STORAGE_HOST}/minio/health/live"
fi

release_dir=""
release_metadata=""
predeploy_backup_path=""
target_image_id=""
backend_switched=false
backend_previous_available=false
backend_previous_was_running=false
caddy_switched=false
caddy_previous_available=false
caddy_previous_was_running=false
caddyfile_previous_state=absent
caddyfile_previous_path=""
deployment_succeeded=false
public_health_checked=false
migration_up_started=false
migration_completed=false
migration_target_version=""
migration_version_before=""
migration_version_after=""
migration_applied_count=""
MIGRATION_STDOUT_PATH=""
REGISTRY_AUTH_DIR=""
RUNTIME_ENV_FILE=""
MIGRATION_RUNTIME_ENV_FILE=""
POSTGRES_RUNTIME_ENV_FILE=""
MINIO_RUNTIME_ENV_FILE=""
POSTGRES_IMAGE_ID=""
MINIO_IMAGE_ID=""
MINIO_MC_IMAGE_ID=""
CADDY_IMAGE_ID=""
backend_switch_abort_recovery_failed=false
caddy_switch_abort_recovery_failed=false
postgres_health_previous_available=false
postgres_health_previous_was_running=false
minio_health_previous_available=false
minio_health_previous_was_running=false

host_port() {
  local binding="$1"
  echo "${binding##*:}"
}

is_immutable_image_ref() {
  [[ "$1" =~ ^[^[:space:]@]+@sha256:[[:xdigit:]]{64}$ ]]
}

append_release_metadata() {
  local key="$1" value="$2"
  if [[ -z "${release_metadata}" ]]; then
    return 0
  fi
  if [[ "${key}" == *$'\n'* || "${value}" == *$'\n'* ]]; then
    echo "release metadata cannot contain newlines" >&2
    return 1
  fi
  printf '%s=%s\n' "${key}" "${value}" >>"${release_metadata}"
}

write_release_result() {
  local result="$1" exit_code="$2"
  if [[ -z "${release_dir}" ]]; then
    return 0
  fi
  local result_tmp
  result_tmp="$(mktemp "${release_dir}/.result.tmp.XXXXXX")"
  {
    printf 'result=%s\n' "${result}"
    printf 'exit_code=%s\n' "${exit_code}"
    printf 'finished_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } >"${result_tmp}"
  chmod 0600 "${result_tmp}"
  mv -f -- "${result_tmp}" "${release_dir}/result.env"
}

validate_database_url_contract() {
  DATABASE_URL="${DATABASE_URL}" \
  POSTGRES_CONTAINER="${POSTGRES_CONTAINER}" \
  POSTGRES_DB="${POSTGRES_DB}" \
  POSTGRES_USER="${POSTGRES_USER}" \
  POSTGRES_PASSWORD="${POSTGRES_PASSWORD}" \
    python3 - <<'PY'
import os
import urllib.parse

def reject(message):
    raise SystemExit(f"DATABASE_URL {message}; refusing to migrate a database not covered by the predeploy backup")

try:
    parsed = urllib.parse.urlsplit(os.environ["DATABASE_URL"])
    hostname = parsed.hostname
    port = parsed.port
    username = urllib.parse.unquote(parsed.username or "")
    password = urllib.parse.unquote(parsed.password or "")
except ValueError as error:
    reject(f"is malformed ({error})")

if parsed.scheme not in {"postgres", "postgresql"}:
    reject("must use the postgres or postgresql URL scheme")
if parsed.fragment:
    reject("must not contain a fragment")
if hostname != os.environ["POSTGRES_CONTAINER"].lower() or port != 5432:
    reject("must target the managed PostgreSQL container on port 5432")
if username != os.environ["POSTGRES_USER"] or password != os.environ["POSTGRES_PASSWORD"]:
    reject("credentials must match POSTGRES_USER and POSTGRES_PASSWORD")
if not parsed.path.startswith("/") or urllib.parse.unquote(parsed.path[1:]) != os.environ["POSTGRES_DB"]:
    reject("database name must match POSTGRES_DB")
query = urllib.parse.parse_qsl(parsed.query, keep_blank_values=True, strict_parsing=True)
if query != [("sslmode", "disable")]:
    reject("query must be exactly sslmode=disable with no routing overrides")
PY
}

preflight_deploy() {
  local command health_url
  for command in docker curl flock mktemp python3; do
    if ! command -v "${command}" >/dev/null 2>&1; then
      echo "required command not found: ${command}" >&2
      return 1
    fi
  done

  if ! validate_database_url_contract; then
    return 1
  fi

  if [[ "${APP_ENV_PATH}" != /* || "${DEPLOY_STATE_DIR}" != /* || "${DEPLOY_LOCK_PATH}" != /* ]]; then
    echo "APP_ENV_PATH, DEPLOY_STATE_DIR, and DEPLOY_LOCK_PATH must be absolute paths" >&2
    return 1
  fi
  if [[ ! "${RELEASE_ID}" =~ ^[A-Za-z0-9._-]+$ ]]; then
    echo "RELEASE_ID may contain only letters, digits, dot, underscore, and dash" >&2
    return 1
  fi
  if [[ ! "${SOURCE_COMMIT}" =~ ^[A-Za-z0-9._-]+$ ]]; then
    echo "SOURCE_COMMIT may contain only letters, digits, dot, underscore, and dash" >&2
    return 1
  fi
  if [[ "${ALLOW_MUTABLE_IMAGE}" != "true" ]] && ! is_immutable_image_ref "${GHCR_IMAGE}"; then
    echo "GHCR_IMAGE must use a sha256 digest; set ALLOW_MUTABLE_IMAGE=true only for an intentional manual exception" >&2
    return 1
  fi
  if [[ ! "${HEALTHCHECK_TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || (( HEALTHCHECK_TIMEOUT_SECONDS > 300 )); then
    echo "HEALTHCHECK_TIMEOUT_SECONDS must be an integer from 1 through 300" >&2
    return 1
  fi
  if [[ ! "${APP_PORT}" =~ ^[1-9][0-9]*$ ]] || (( 10#${APP_PORT} > 65535 )) || \
    [[ "${APP_PORT}" != "${PORT:-8080}" ]]; then
    echo "APP_PORT must be a valid port and match PORT (or the API default 8080)" >&2
    return 1
  fi
  for image_ref in "${POSTGRES_IMAGE}" "${MINIO_IMAGE}" "${MINIO_MC_IMAGE}" "${CADDY_IMAGE}"; do
    if ! is_immutable_image_ref "${image_ref}"; then
      echo "POSTGRES_IMAGE, MINIO_IMAGE, MINIO_MC_IMAGE, and CADDY_IMAGE must all use sha256 digest references" >&2
      return 1
    fi
  done
  if [[ "${SKIP_INFRA_IMAGE_PULL}" != "true" && "${SKIP_INFRA_IMAGE_PULL}" != "false" ]]; then
    echo "SKIP_INFRA_IMAGE_PULL must be exactly true or false" >&2
    return 1
  fi
  for health_url in "${HEALTHCHECK_URL:-}" "${PUBLIC_API_HEALTHCHECK_URL:-}" "${PUBLIC_STORAGE_HEALTHCHECK_URL:-}" "${FRONTEND_HEALTHCHECK_URL:-}"; do
    if [[ -n "${health_url}" && ( "${health_url}" != http://* && "${health_url}" != https://* || "${health_url}" == *[$'\n\r?#@']* ) ]]; then
      echo "health-check URLs must be plain http(s) URLs without credentials, query strings, or fragments" >&2
      return 1
    fi
  done

  mkdir -p "${DEPLOY_STATE_DIR}" "${RELEASES_DIR}" "${PREDEPLOY_BACKUP_DIR}"
  chmod 0700 "${DEPLOY_STATE_DIR}" "${RELEASES_DIR}" "${PREDEPLOY_BACKUP_DIR}"
  exec 9>"${DEPLOY_LOCK_PATH}"
  if ! flock -n 9; then
    echo "another SCC deploy is already running" >&2
    return 1
  fi

  if container_exists "${CANDIDATE_CONTAINER}" || container_exists "${PREVIOUS_CONTAINER}" || \
    container_exists "${CADDY_PREVIOUS_CONTAINER}" || container_exists "${POSTGRES_HEALTH_PREVIOUS_CONTAINER}" || \
    container_exists "${MINIO_HEALTH_PREVIOUS_CONTAINER}"; then
    echo "reserved deploy container exists; reconcile candidate/previous containers before retrying" >&2
    return 1
  fi

  release_dir="${RELEASES_DIR}/${RELEASE_ID}"
  if [[ -e "${release_dir}" ]]; then
    echo "release metadata already exists: ${release_dir}" >&2
    return 1
  fi
  mkdir "${release_dir}"
  chmod 0700 "${release_dir}"
  release_metadata="${release_dir}/release.env"
  : >"${release_metadata}"
  chmod 0600 "${release_metadata}"
  append_release_metadata release_id "${RELEASE_ID}"
  append_release_metadata source_commit "${SOURCE_COMMIT}"
  append_release_metadata started_utc "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  append_release_metadata target_image_ref "${GHCR_IMAGE}"
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

require_container_image_id() {
  local container="$1" expected_image_id="$2" actual_image_id
  actual_image_id="$(docker inspect -f '{{.Image}}' "${container}")"
  if [[ -z "${actual_image_id}" || "${actual_image_id}" != "${expected_image_id}" ]]; then
    echo "${container} image does not match the configured immutable infrastructure image; reconcile it before deploying" >&2
    return 1
  fi
}

require_container_named_volume() {
  local container="$1" expected_volume="$2" expected_destination="$3" mounts line matches=0
  if ! mounts="$(docker inspect -f '{{range .Mounts}}{{printf "%s|%s|%s|%t\n" .Type .Name .Destination .RW}}{{end}}' "${container}")"; then
    echo "${container} mounts could not be inspected; refusing to deploy against unverified persistent storage" >&2
    return 1
  fi
  while IFS= read -r line; do
    if [[ "${line}" == "volume|${expected_volume}|${expected_destination}|true" ]]; then
      matches=$((matches + 1))
    fi
  done <<<"${mounts}"
  if [[ "${matches}" -ne 1 ]]; then
    echo "${container} must mount writable named volume ${expected_volume} at ${expected_destination}; refusing to deploy against storage drift" >&2
    return 1
  fi
}

wait_for_postgres() {
  for _ in $(seq 1 60); do
    if docker exec "${POSTGRES_CONTAINER}" pg_isready -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "${POSTGRES_CONTAINER} failed readiness check" >&2
  return 1
}

wait_for_minio() {
  local minio_healthcheck_url="${MINIO_HEALTHCHECK_URL:-http://127.0.0.1:$(host_port "${MINIO_API_HOST_PORT}")/minio/health/live}"
  for _ in $(seq 1 60); do
    if curl --fail --silent --show-error --max-time "${HEALTHCHECK_TIMEOUT_SECONDS}" "${minio_healthcheck_url}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "${MINIO_CONTAINER} failed readiness check" >&2
  return 1
}

container_healthcheck_is_current() {
  local container="$1" expected_command="$2" expected_revision="$3" actual
  if ! actual="$(docker inspect -f '{{if .Config.Healthcheck}}{{index .Config.Healthcheck.Test 0}}|{{index .Config.Healthcheck.Test 1}}|{{index .Config.Labels "io.smartcover.healthcheck"}}{{else}}none||{{end}}' "${container}" 2>/dev/null)"; then
    return 1
  fi
  [[ "${actual}" == "CMD-SHELL|${expected_command}|${expected_revision}" ]]
}

create_postgres_container() {
  docker run -d \
    --name "${POSTGRES_CONTAINER}" \
    --restart unless-stopped \
    --network "${DOCKER_NETWORK}" \
    --env-file "${POSTGRES_RUNTIME_ENV_FILE}" \
    --label "${HEALTHCHECK_LABEL_KEY}=${POSTGRES_HEALTHCHECK_REVISION}" \
    --health-cmd "${POSTGRES_HEALTHCHECK_COMMAND}" \
    --health-interval 10s \
    --health-timeout 5s \
    --health-retries 5 \
    --health-start-period 60s \
    -v "${POSTGRES_VOLUME}:/var/lib/postgresql/data" \
    "${POSTGRES_IMAGE_ID}" >/dev/null
}

create_minio_container() {
  docker run -d \
    --name "${MINIO_CONTAINER}" \
    --restart unless-stopped \
    --network "${DOCKER_NETWORK}" \
    -p "${MINIO_API_HOST_PORT}:9000" \
    -p "${MINIO_CONSOLE_HOST_PORT}:9001" \
    --env-file "${MINIO_RUNTIME_ENV_FILE}" \
    --label "${HEALTHCHECK_LABEL_KEY}=${MINIO_HEALTHCHECK_REVISION}" \
    --health-cmd "${MINIO_HEALTHCHECK_COMMAND}" \
    --health-interval 30s \
    --health-timeout 10s \
    --health-retries 3 \
    --health-start-period 30s \
    -v "${MINIO_VOLUME}:/data" \
    "${MINIO_IMAGE_ID}" server /data --console-address ':9001' >/dev/null
}

ensure_postgres() {
  docker volume create "${POSTGRES_VOLUME}" >/dev/null
  if container_exists "${POSTGRES_CONTAINER}"; then
    require_container_image_id "${POSTGRES_CONTAINER}" "${POSTGRES_IMAGE_ID}"
    require_container_named_volume "${POSTGRES_CONTAINER}" "${POSTGRES_VOLUME}" /var/lib/postgresql/data
    ensure_container_network "${POSTGRES_CONTAINER}"
    if ! container_running "${POSTGRES_CONTAINER}"; then
      docker start "${POSTGRES_CONTAINER}" >/dev/null
    fi
  else
    create_postgres_container
  fi
  wait_for_postgres
}

ensure_minio() {
  docker volume create "${MINIO_VOLUME}" >/dev/null
  if container_exists "${MINIO_CONTAINER}"; then
    require_container_image_id "${MINIO_CONTAINER}" "${MINIO_IMAGE_ID}"
    require_container_named_volume "${MINIO_CONTAINER}" "${MINIO_VOLUME}" /data
    ensure_container_network "${MINIO_CONTAINER}"
    if ! container_running "${MINIO_CONTAINER}"; then
      docker start "${MINIO_CONTAINER}" >/dev/null
    fi
  else
    create_minio_container
  fi
  wait_for_minio
}

restore_infrastructure_previous() {
  local active="$1" previous="$2" was_running="$3" wait_function="$4"
  docker rm -f "${active}" >/dev/null 2>&1 || true
  docker rename "${previous}" "${active}" || return 1
  if [[ "${was_running}" == "true" ]]; then
    docker start "${active}" >/dev/null || return 1
    "${wait_function}"
  fi
}

reconcile_postgres_healthcheck() {
  if container_healthcheck_is_current "${POSTGRES_CONTAINER}" "${POSTGRES_HEALTHCHECK_COMMAND}" "${POSTGRES_HEALTHCHECK_REVISION}"; then
    append_release_metadata postgres_healthcheck unchanged
    return 0
  fi
  container_running "${POSTGRES_CONTAINER}" && postgres_health_previous_was_running=true
  if [[ "${postgres_health_previous_was_running}" == "true" ]]; then
    docker stop "${POSTGRES_CONTAINER}" >/dev/null || return 1
  fi
  if ! docker rename "${POSTGRES_CONTAINER}" "${POSTGRES_HEALTH_PREVIOUS_CONTAINER}"; then
    [[ "${postgres_health_previous_was_running}" != "true" ]] || docker start "${POSTGRES_CONTAINER}" >/dev/null 2>&1 || true
    return 1
  fi
  postgres_health_previous_available=true
  if ! create_postgres_container || ! wait_for_postgres; then
    if ! restore_infrastructure_previous "${POSTGRES_CONTAINER}" "${POSTGRES_HEALTH_PREVIOUS_CONTAINER}" "${postgres_health_previous_was_running}" wait_for_postgres; then
      append_release_metadata postgres_healthcheck rollback-failed
      echo "CRITICAL: PostgreSQL healthcheck reconciliation and rollback both failed" >&2
      return 90
    fi
    postgres_health_previous_available=false
    append_release_metadata postgres_healthcheck failed-original-restored
    echo "PostgreSQL healthcheck reconciliation failed; the original container was restored" >&2
    return 1
  fi
  if ! docker rm "${POSTGRES_HEALTH_PREVIOUS_CONTAINER}" >/dev/null; then
    echo "WARNING: PostgreSQL healthcheck is active but ${POSTGRES_HEALTH_PREVIOUS_CONTAINER} could not be removed" >&2
    append_release_metadata postgres_healthcheck reconciled-previous-retained
    postgres_health_previous_available=false
    return 0
  fi
  postgres_health_previous_available=false
  append_release_metadata postgres_healthcheck reconciled
}

reconcile_minio_healthcheck() {
  if container_healthcheck_is_current "${MINIO_CONTAINER}" "${MINIO_HEALTHCHECK_COMMAND}" "${MINIO_HEALTHCHECK_REVISION}"; then
    append_release_metadata minio_healthcheck unchanged
    return 0
  fi
  container_running "${MINIO_CONTAINER}" && minio_health_previous_was_running=true
  if [[ "${minio_health_previous_was_running}" == "true" ]]; then
    docker stop "${MINIO_CONTAINER}" >/dev/null || return 1
  fi
  if ! docker rename "${MINIO_CONTAINER}" "${MINIO_HEALTH_PREVIOUS_CONTAINER}"; then
    [[ "${minio_health_previous_was_running}" != "true" ]] || docker start "${MINIO_CONTAINER}" >/dev/null 2>&1 || true
    return 1
  fi
  minio_health_previous_available=true
  if ! create_minio_container || ! wait_for_minio; then
    if ! restore_infrastructure_previous "${MINIO_CONTAINER}" "${MINIO_HEALTH_PREVIOUS_CONTAINER}" "${minio_health_previous_was_running}" wait_for_minio; then
      append_release_metadata minio_healthcheck rollback-failed
      echo "CRITICAL: MinIO healthcheck reconciliation and rollback both failed" >&2
      return 90
    fi
    minio_health_previous_available=false
    append_release_metadata minio_healthcheck failed-original-restored
    echo "MinIO healthcheck reconciliation failed; the original container was restored" >&2
    return 1
  fi
  if ! docker rm "${MINIO_HEALTH_PREVIOUS_CONTAINER}" >/dev/null; then
    echo "WARNING: MinIO healthcheck is active but ${MINIO_HEALTH_PREVIOUS_CONTAINER} could not be removed" >&2
    append_release_metadata minio_healthcheck reconciled-previous-retained
    minio_health_previous_available=false
    return 0
  fi
  minio_health_previous_available=false
  append_release_metadata minio_healthcheck reconciled
}

reconcile_infrastructure_healthchecks() {
  reconcile_postgres_healthcheck
  reconcile_minio_healthcheck
}

configure_minio_bucket() {
  local cors_file mc_script configure_output configure_result verify_output verify_result
  cors_file="$(mktemp)"
  mc_script="$(mktemp)"
  cleanup_minio_config() {
    rm -f "${cors_file}" "${mc_script}"
    trap - RETURN
  }
  trap cleanup_minio_config RETURN
  if [[ "${MINIO_ROOT_USER}" == *$'\n'* || "${MINIO_ROOT_PASSWORD}" == *$'\n'* ]]; then
    echo "MinIO credentials cannot contain newlines" >&2
    return 1
  fi
  CORS_ORIGINS="${CORS_ORIGINS}" python3 - "${cors_file}" <<'PY'
import os, sys
from xml.sax.saxutils import escape
origins = [origin.strip() for origin in os.environ.get("CORS_ORIGINS", "").split(",") if origin.strip()]
if not origins or "*" in origins:
    raise SystemExit("explicit MinIO CORS origins are required")
parts = ['<CORSConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">', '<CORSRule>']
for origin in origins:
    parts.append(f'<AllowedOrigin>{escape(origin)}</AllowedOrigin>')
for method in ["GET", "PUT", "HEAD"]:
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
set -efu

minio_container="$1"
minio_bucket="$2"
cors_file="${3:-/tmp/cors.xml}"
IFS= read -r minio_user
IFS= read -r minio_password
desired_origins="${MINIO_CORS_ORIGINS:?MINIO_CORS_ORIGINS is required}"

read_global_cors_origins() {
  global_config="$(mc admin config get scc api 2>/dev/null)" || return 1
  global_origins=''
  for token in ${global_config}; do
    case "${token}" in
      cors_allow_origin=*)
        global_origins="${token#cors_allow_origin=}"
        ;;
    esac
  done
  [ -n "${global_origins}" ] || return 1
  printf '%s\n' "${global_origins}"
}

configure_global_cors() {
  current_origins="$(read_global_cors_origins)" || return 1
  if [ "${current_origins}" = "${desired_origins}" ]; then
    printf '%s\n' 'SCC_MINIO_CORS_RESULT=global-unchanged'
    return 0
  fi
  mc admin config set scc api "cors_allow_origin=${desired_origins}" >/dev/null || return 1
  current_origins="$(read_global_cors_origins)" || return 1
  [ "${current_origins}" = "${desired_origins}" ] || return 1
  printf '%s\n' 'SCC_MINIO_CORS_RESULT=global-changed'
}

mc alias set scc "http://${minio_container}:9000" "${minio_user}" "${minio_password}"

if [ "${MINIO_CORS_VERIFY_ONLY:-false}" = 'true' ]; then
  current_origins="$(read_global_cors_origins)" || {
    echo 'MinIO global API CORS configuration could not be read after restart.' >&2
    exit 1
  }
  if [ "${current_origins}" != "${desired_origins}" ]; then
    echo 'MinIO global API CORS configuration drifted after restart.' >&2
    exit 1
  fi
  printf '%s\n' 'SCC_MINIO_CORS_RESULT=global-verified'
  exit 0
fi

mc mb --ignore-existing "scc/${minio_bucket}"
mc anonymous set none "scc/${minio_bucket}"
case "${MINIO_CONFIGURE_CORS:-best-effort}" in
  false|off|skip)
    echo "Skipping MinIO CORS configuration because MINIO_CONFIGURE_CORS=${MINIO_CONFIGURE_CORS}" >&2
    printf '%s\n' 'SCC_MINIO_CORS_RESULT=skipped'
    ;;
  required|best-effort|true|on)
    if mc cors set "scc/${minio_bucket}" "${cors_file}"; then
      printf '%s\n' 'SCC_MINIO_CORS_RESULT=bucket'
    else
      echo 'WARNING: MinIO bucket CORS is unavailable; trying the supported global API cors_allow_origin fallback.' >&2
      if ! configure_global_cors; then
        if [ "${MINIO_CONFIGURE_CORS}" = 'required' ]; then
          echo 'MinIO CORS is required, and both bucket CORS and the global API fallback failed.' >&2
          exit 1
        fi
        echo 'WARNING: MinIO CORS configuration failed; the bucket remains private, continuing deploy.' >&2
        echo 'Set MINIO_CONFIGURE_CORS=required to make failure of both CORS mechanisms fatal.' >&2
        printf '%s\n' 'SCC_MINIO_CORS_RESULT=unconfigured'
      fi
    fi
    ;;
esac
SH
  chmod 700 "${mc_script}"

  if ! configure_output="$(printf '%s\n%s\n' "${MINIO_ROOT_USER}" "${MINIO_ROOT_PASSWORD}" | docker run --rm -i \
    --network "${DOCKER_NETWORK}" \
    -e "MINIO_CONFIGURE_CORS=${MINIO_CONFIGURE_CORS}" \
    -e "MINIO_CORS_ORIGINS=${CORS_ORIGINS}" \
    -v "${cors_file}:/tmp/cors.xml:ro" \
    -v "${mc_script}:/tmp/configure-minio.sh:ro" \
    --entrypoint /bin/sh \
    "${MINIO_MC_IMAGE_ID}" \
    /tmp/configure-minio.sh \
    "${MINIO_CONTAINER}" \
    "${MINIO_BUCKET}" \
    /tmp/cors.xml)"; then
    return 1
  fi
  configure_result="$(printf '%s\n' "${configure_output}" | awk -F= '/^SCC_MINIO_CORS_RESULT=/{value=$2} END{print value}')"
  case "${configure_result}" in
    bucket)
      append_release_metadata minio_cors_result bucket
      append_release_metadata minio_cors_restart not-required
      echo "MinIO CORS configured on bucket ${MINIO_BUCKET}; no MinIO restart required."
      ;;
    global-unchanged)
      append_release_metadata minio_cors_result global-unchanged
      append_release_metadata minio_cors_restart not-required
      echo "MinIO bucket CORS is unsupported; exact global API CORS is already configured, so no restart is required."
      ;;
    global-changed)
      echo "MinIO bucket CORS is unsupported; restarting ${MINIO_CONTAINER} once to activate exact global API CORS."
      docker restart "${MINIO_CONTAINER}" >/dev/null
      wait_for_minio
      if ! verify_output="$(printf '%s\n%s\n' "${MINIO_ROOT_USER}" "${MINIO_ROOT_PASSWORD}" | docker run --rm -i \
        --network "${DOCKER_NETWORK}" \
        -e "MINIO_CONFIGURE_CORS=${MINIO_CONFIGURE_CORS}" \
        -e "MINIO_CORS_ORIGINS=${CORS_ORIGINS}" \
        -e MINIO_CORS_VERIFY_ONLY=true \
        -v "${cors_file}:/tmp/cors.xml:ro" \
        -v "${mc_script}:/tmp/configure-minio.sh:ro" \
        --entrypoint /bin/sh \
        "${MINIO_MC_IMAGE_ID}" \
        /tmp/configure-minio.sh \
        "${MINIO_CONTAINER}" \
        "${MINIO_BUCKET}" \
        /tmp/cors.xml)"; then
        echo "MinIO did not retain the exact global API CORS configuration after restart" >&2
        return 1
      fi
      verify_result="$(printf '%s\n' "${verify_output}" | awk -F= '/^SCC_MINIO_CORS_RESULT=/{value=$2} END{print value}')"
      if [[ "${verify_result}" != "global-verified" ]]; then
        echo "MinIO global API CORS verification returned an invalid result" >&2
        return 1
      fi
      append_release_metadata minio_cors_result global-verified
      append_release_metadata minio_cors_restart performed
      ;;
    skipped)
      append_release_metadata minio_cors_result skipped
      append_release_metadata minio_cors_restart not-required
      ;;
    unconfigured)
      append_release_metadata minio_cors_result unconfigured-best-effort
      append_release_metadata minio_cors_restart not-required
      ;;
    *)
      echo "MinIO CORS configuration returned an invalid result" >&2
      return 1
      ;;
  esac
}

wait_for_url() {
  local label="$1" url="$2" attempts="${3:-30}"
  if [[ -z "${url}" ]]; then
    return 0
  fi
  for _ in $(seq 1 "${attempts}"); do
    if curl --fail --silent --show-error --max-time "${HEALTHCHECK_TIMEOUT_SECONDS}" "${url}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "${label} failed health check: ${url}" >&2
  return 1
}

create_predeploy_backup() {
  local backup_tmp
  predeploy_backup_path="${PREDEPLOY_BACKUP_DIR}/${RELEASE_ID}.dump"
  backup_tmp="$(mktemp "${PREDEPLOY_BACKUP_DIR}/.${RELEASE_ID}.dump.tmp.XXXXXX")"
  if ! docker exec "${POSTGRES_CONTAINER}" \
    pg_dump \
    --username "${POSTGRES_USER}" \
    --dbname "${POSTGRES_DB}" \
    --format custom \
    --no-owner \
    --no-acl >"${backup_tmp}"; then
    rm -f -- "${backup_tmp}"
    if [[ "${PREDEPLOY_BACKUP_REQUIRED}" == "true" ]]; then
      echo "predeploy PostgreSQL backup failed; deploy stopped before starting the candidate" >&2
      return 1
    fi
    append_release_metadata predeploy_backup optional-failed
    return 0
  fi
  if [[ ! -s "${backup_tmp}" ]]; then
    rm -f -- "${backup_tmp}"
    if [[ "${PREDEPLOY_BACKUP_REQUIRED}" == "true" ]]; then
      echo "predeploy PostgreSQL backup is empty; deploy stopped before starting the candidate" >&2
      return 1
    fi
    append_release_metadata predeploy_backup optional-empty
    return 0
  fi
  if ! docker exec -i "${POSTGRES_CONTAINER}" pg_restore --list <"${backup_tmp}" >/dev/null; then
    rm -f -- "${backup_tmp}"
    echo "predeploy PostgreSQL backup failed pg_restore validation; deploy stopped before migrations" >&2
    return 1
  fi
  chmod 0600 "${backup_tmp}"
  mv -f -- "${backup_tmp}" "${predeploy_backup_path}"
  append_release_metadata predeploy_backup "${predeploy_backup_path}"
}

prepare_runtime_env_file() {
  local index
  RUNTIME_ENV_FILE="$(mktemp "${DEPLOY_STATE_DIR}/.runtime-env.XXXXXX")"
  MIGRATION_RUNTIME_ENV_FILE="$(mktemp "${DEPLOY_STATE_DIR}/.migration-env.XXXXXX")"
  POSTGRES_RUNTIME_ENV_FILE="$(mktemp "${DEPLOY_STATE_DIR}/.postgres-env.XXXXXX")"
  MINIO_RUNTIME_ENV_FILE="$(mktemp "${DEPLOY_STATE_DIR}/.minio-env.XXXXXX")"
  chmod 0600 "${RUNTIME_ENV_FILE}" "${MIGRATION_RUNTIME_ENV_FILE}" "${POSTGRES_RUNTIME_ENV_FILE}" "${MINIO_RUNTIME_ENV_FILE}"
  if ! {
    for ((index = 0; index < ${#APP_ENV_KEYS[@]}; index++)); do
      if is_application_runtime_env_key "${APP_ENV_KEYS[index]}"; then
        printf '%s=%s\n' "${APP_ENV_KEYS[index]}" "${APP_ENV_VALUES[index]}"
      fi
    done
  } >"${RUNTIME_ENV_FILE}"; then
    cleanup_runtime_env
    return 1
  fi
  if ! {
    printf 'DATABASE_URL=%s\n' "${DATABASE_URL}"
    for ((index = 0; index < ${#APP_ENV_KEYS[@]}; index++)); do
      if [[ "${APP_ENV_KEYS[index]}" == "MIGRATION_TIMEOUT" ]]; then
        printf 'MIGRATION_TIMEOUT=%s\n' "${APP_ENV_VALUES[index]}"
      fi
    done
  } >"${MIGRATION_RUNTIME_ENV_FILE}"; then
    cleanup_runtime_env
    return 1
  fi
  if ! {
    printf 'POSTGRES_DB=%s\n' "${POSTGRES_DB}"
    printf 'POSTGRES_USER=%s\n' "${POSTGRES_USER}"
    printf 'POSTGRES_PASSWORD=%s\n' "${POSTGRES_PASSWORD}"
  } >"${POSTGRES_RUNTIME_ENV_FILE}"; then
    cleanup_runtime_env
    return 1
  fi
  if ! {
    printf 'MINIO_ROOT_USER=%s\n' "${MINIO_ROOT_USER}"
    printf 'MINIO_ROOT_PASSWORD=%s\n' "${MINIO_ROOT_PASSWORD}"
  } >"${MINIO_RUNTIME_ENV_FILE}"; then
    cleanup_runtime_env
    return 1
  fi
}

cleanup_runtime_env() {
  local path
  for path in "${RUNTIME_ENV_FILE}" "${MIGRATION_RUNTIME_ENV_FILE}" "${POSTGRES_RUNTIME_ENV_FILE}" "${MINIO_RUNTIME_ENV_FILE}"; do
    [[ -z "${path}" ]] || rm -f -- "${path}"
  done
  RUNTIME_ENV_FILE=""
  MIGRATION_RUNTIME_ENV_FILE=""
  POSTGRES_RUNTIME_ENV_FILE=""
  MINIO_RUNTIME_ENV_FILE=""
}

prepare_target_image() {
  if [[ -n "${GHCR_USERNAME:-}" && -n "${GHCR_TOKEN:-}" ]]; then
    REGISTRY_AUTH_DIR="$(mktemp -d "${DEPLOY_STATE_DIR}/.registry-auth.XXXXXX")"
    chmod 0700 "${REGISTRY_AUTH_DIR}"
    printf '%s' "${GHCR_TOKEN}" | DOCKER_CONFIG="${REGISTRY_AUTH_DIR}" docker login ghcr.io -u "${GHCR_USERNAME}" --password-stdin >/dev/null
  fi
  if [[ "${SKIP_IMAGE_PULL}" != "true" ]]; then
    if [[ -n "${REGISTRY_AUTH_DIR}" ]]; then
      DOCKER_CONFIG="${REGISTRY_AUTH_DIR}" docker pull "${GHCR_IMAGE}" >/dev/null
    else
      docker pull "${GHCR_IMAGE}" >/dev/null
    fi
  fi
  unset GHCR_TOKEN
  cleanup_registry_auth
  target_image_id="$(docker image inspect -f '{{.Id}}' "${GHCR_IMAGE}")"
  if [[ -z "${target_image_id}" ]]; then
    echo "could not resolve target image ID" >&2
    return 1
  fi
  append_release_metadata target_image_id "${target_image_id}"
  if container_exists "${CONTAINER_NAME}"; then
    append_release_metadata previous_image_ref "$(docker inspect -f '{{.Config.Image}}' "${CONTAINER_NAME}")"
    append_release_metadata previous_image_id "$(docker inspect -f '{{.Image}}' "${CONTAINER_NAME}")"
  else
    append_release_metadata previous_image_ref none
    append_release_metadata previous_image_id none
  fi
}

cleanup_registry_auth() {
  if [[ -n "${REGISTRY_AUTH_DIR}" && -d "${REGISTRY_AUTH_DIR}" ]]; then
    rm -rf -- "${REGISTRY_AUTH_DIR}"
  fi
  REGISTRY_AUTH_DIR=""
}

prepare_infrastructure_images() {
  local image_ref
  if [[ "${SKIP_INFRA_IMAGE_PULL}" != "true" ]]; then
    for image_ref in "${POSTGRES_IMAGE}" "${MINIO_IMAGE}" "${MINIO_MC_IMAGE}" "${CADDY_IMAGE}"; do
      docker pull "${image_ref}" >/dev/null
    done
  fi
  POSTGRES_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "${POSTGRES_IMAGE}")"
  MINIO_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "${MINIO_IMAGE}")"
  MINIO_MC_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "${MINIO_MC_IMAGE}")"
  CADDY_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "${CADDY_IMAGE}")"
  for image_ref in "${POSTGRES_IMAGE_ID}" "${MINIO_IMAGE_ID}" "${MINIO_MC_IMAGE_ID}" "${CADDY_IMAGE_ID}"; do
    if [[ ! "${image_ref}" =~ ^sha256:[[:xdigit:]]{64}$ ]]; then
      echo "could not resolve an immutable infrastructure image ID" >&2
      return 1
    fi
  done
  append_release_metadata postgres_image_ref "${POSTGRES_IMAGE}"
  append_release_metadata postgres_image_id "${POSTGRES_IMAGE_ID}"
  append_release_metadata minio_image_ref "${MINIO_IMAGE}"
  append_release_metadata minio_image_id "${MINIO_IMAGE_ID}"
  append_release_metadata minio_mc_image_ref "${MINIO_MC_IMAGE}"
  append_release_metadata minio_mc_image_id "${MINIO_MC_IMAGE_ID}"
  append_release_metadata caddy_image_ref "${CADDY_IMAGE}"
  append_release_metadata caddy_image_id "${CADDY_IMAGE_ID}"
}

inspect_migration_json() {
  local output_path="$1" mode="$2"
  python3 - "${output_path}" "${mode}" <<'PY'
import json
import re
import sys

path, mode = sys.argv[1:3]
try:
    with open(path, "r", encoding="utf-8") as source:
        payload = json.load(source)
except (OSError, UnicodeError, json.JSONDecodeError) as error:
    raise SystemExit(f"invalid {mode} migration output: {error}") from error

if not isinstance(payload, dict):
    raise SystemExit(f"invalid {mode} migration output: expected an object")

def integer(value, label, *, allow_zero=True):
    if isinstance(value, bool) or not isinstance(value, int) or (value < 0 if allow_zero else value <= 0):
        raise SystemExit(f"invalid {mode} migration output: {label}")
    return value

def migrations():
    items = payload.get("migrations")
    if not isinstance(items, list) or not items:
        raise SystemExit(f"invalid {mode} migration output: migrations")
    return items

if mode == "validate":
    if payload.get("valid") is not True:
        raise SystemExit("migration manifest validation did not pass")
    versions = []
    for item in migrations():
        if not isinstance(item, dict):
            raise SystemExit("invalid validate migration output: migration entry")
        versions.append(integer(item.get("version"), "migration version", allow_zero=False))
        if not re.fullmatch(r"\d{14}_[a-z0-9_]+\.sql", item.get("name", "")):
            raise SystemExit("invalid validate migration output: migration name")
        if not re.fullmatch(r"[a-f0-9]{64}", item.get("checksum", "")):
            raise SystemExit("invalid validate migration output: migration checksum")
    if versions != sorted(versions) or len(versions) != len(set(versions)):
        raise SystemExit("invalid validate migration output: migration ordering")
    print(versions[-1])
elif mode == "check":
    if payload.get("ok") is not True or payload.get("violations") != []:
        raise SystemExit("migration invariant check did not pass")
    print("ok")
elif mode in {"status_before", "status_after"}:
    current = integer(payload.get("currentVersion"), "currentVersion")
    items = migrations()
    states = []
    for item in items:
        if not isinstance(item, dict):
            raise SystemExit(f"invalid {mode} migration output: migration entry")
        integer(item.get("version"), "migration version", allow_zero=False)
        state = item.get("state")
        if state not in {"applied", "pending"}:
            raise SystemExit(f"invalid {mode} migration output: unsafe migration state")
        states.append(state)
    if mode == "status_after" and (payload.get("ledgerExists") is not True or any(state != "applied" for state in states)):
        raise SystemExit("post-migration ledger is not fully applied")
    print(current)
elif mode == "up":
    if payload.get("ok") is not True:
        raise SystemExit("migration up did not pass")
    applied = payload.get("applied")
    applied_count = integer(payload.get("appliedCount"), "appliedCount")
    if applied is None and applied_count == 0:
        applied = []
    if not isinstance(applied, list):
        raise SystemExit("invalid up migration output: applied")
    if applied_count != len(applied):
        raise SystemExit("invalid up migration output: appliedCount mismatch")
    current = integer(payload.get("currentVersion"), "currentVersion", allow_zero=False)
    print(f"{current}|{applied_count}")
elif mode == "version":
    if payload.get("ledgerExists") is not True:
        raise SystemExit("migration version ledger does not exist")
    print(integer(payload.get("currentVersion"), "currentVersion", allow_zero=False))
else:
    raise SystemExit("unknown migration output mode")
PY
}

run_migration_command() {
  local label="$1" command="$2" stdout_path stderr_path stdout_tmp stderr_tmp exit_code=0
  if [[ ! "${label}" =~ ^[a-z_]+$ ]]; then
    echo "invalid migration command label" >&2
    return 1
  fi
  case "${command}" in
    validate|check|status|up|version) ;;
    *)
      echo "invalid migration command" >&2
      return 1
      ;;
  esac
  stdout_path="${release_dir}/migration_${label}.json"
  stderr_path="${release_dir}/migration_${label}.stderr"
  stdout_tmp="$(mktemp "${release_dir}/.migration_${label}.stdout.XXXXXX")"
  stderr_tmp="$(mktemp "${release_dir}/.migration_${label}.stderr.XXXXXX")"
  chmod 0600 "${stdout_tmp}" "${stderr_tmp}"

  if docker run --rm \
    --network "${DOCKER_NETWORK}" \
    --env-file "${MIGRATION_RUNTIME_ENV_FILE}" \
    -e AUTO_MIGRATE=false \
    -e SEED_DATA=false \
    -e RUN_BACKGROUND_JOBS=false \
    --entrypoint /app/scc-migrate \
    "${target_image_id}" "${command}" >"${stdout_tmp}" 2>"${stderr_tmp}"; then
    exit_code=0
  else
    exit_code=$?
  fi
  mv -f -- "${stdout_tmp}" "${stdout_path}"
  mv -f -- "${stderr_tmp}" "${stderr_path}"
  append_release_metadata "migration_${label}_stdout" "${stdout_path}"
  append_release_metadata "migration_${label}_stderr" "${stderr_path}"
  if [[ "${exit_code}" -ne 0 ]]; then
    append_release_metadata "migration_${label}_result" failed
    append_release_metadata migration_failed_step "${label}"
    echo "migration ${label} failed; protected output retained in ${release_dir}" >&2
    return "${exit_code}"
  fi
  append_release_metadata "migration_${label}_result" passed
  MIGRATION_STDOUT_PATH="${stdout_path}"
}

record_migration_output_failure() {
  local label="$1"
  append_release_metadata migration_failed_step "${label}_output_validation"
  echo "migration ${label} returned malformed or unsafe output; protected output retained in ${release_dir}" >&2
}

run_migration_contract() {
  local parsed up_version verified_version

  run_migration_command validate validate
  if ! migration_target_version="$(inspect_migration_json "${MIGRATION_STDOUT_PATH}" validate)"; then
    record_migration_output_failure validate
    return 1
  fi
  append_release_metadata migration_target_version "${migration_target_version}"

  run_migration_command check check
  if ! inspect_migration_json "${MIGRATION_STDOUT_PATH}" check >/dev/null; then
    record_migration_output_failure check
    return 1
  fi

  run_migration_command status_before status
  if ! migration_version_before="$(inspect_migration_json "${MIGRATION_STDOUT_PATH}" status_before)"; then
    record_migration_output_failure status_before
    return 1
  fi
  append_release_metadata migration_version_before "${migration_version_before}"

  migration_up_started=true
  run_migration_command up up
  if ! parsed="$(inspect_migration_json "${MIGRATION_STDOUT_PATH}" up)"; then
    record_migration_output_failure up
    return 1
  fi
  IFS='|' read -r up_version migration_applied_count <<<"${parsed}"
  append_release_metadata migration_up_version "${up_version}"
  append_release_metadata migration_applied_count "${migration_applied_count}"

  run_migration_command status_after status
  if ! migration_version_after="$(inspect_migration_json "${MIGRATION_STDOUT_PATH}" status_after)"; then
    record_migration_output_failure status_after
    return 1
  fi
  append_release_metadata migration_version_after "${migration_version_after}"

  run_migration_command version version
  if ! verified_version="$(inspect_migration_json "${MIGRATION_STDOUT_PATH}" version)"; then
    record_migration_output_failure version
    return 1
  fi
  append_release_metadata migration_version_verified "${verified_version}"

  if [[ "${up_version}" != "${migration_target_version}" || "${migration_version_after}" != "${migration_target_version}" || "${verified_version}" != "${migration_target_version}" ]]; then
    append_release_metadata migration_failed_step version_mismatch
    echo "migration version evidence does not match the target image manifest" >&2
    return 1
  fi
  append_release_metadata migration_contract passed
  migration_completed=true
}

run_backend_candidate() {
  if ! docker run -d \
    --name "${CANDIDATE_CONTAINER}" \
    --restart no \
    --network "${DOCKER_NETWORK}" \
    --env-file "${RUNTIME_ENV_FILE}" \
    -e PORT="${APP_PORT}" \
    -e AUTO_MIGRATE=false \
    -e SEED_DATA=false \
    -e RUN_BACKGROUND_JOBS=false \
    --label "${HEALTHCHECK_LABEL_KEY}=${BACKEND_HEALTHCHECK_REVISION}" \
    --health-cmd "${BACKEND_HEALTHCHECK_COMMAND}" \
    --health-interval 30s \
    --health-timeout 10s \
    --health-retries 3 \
    --health-start-period 30s \
    -p "${CANDIDATE_HOST_PORT}:${APP_PORT}" \
    "${target_image_id}" >/dev/null; then
    echo "backend candidate could not start; current backend was not touched" >&2
    return 1
  fi
  local candidate_healthcheck_url
  candidate_healthcheck_url="http://127.0.0.1:$(host_port "${CANDIDATE_HOST_PORT}")/api/v1/health"
  if ! wait_for_url "backend candidate" "${candidate_healthcheck_url}"; then
    docker rm -f "${CANDIDATE_CONTAINER}" >/dev/null 2>&1 || true
    echo "backend candidate failed; current backend was not touched" >&2
    return 1
  fi
  docker rm -f "${CANDIDATE_CONTAINER}" >/dev/null
  append_release_metadata candidate_health passed
}

switch_backend() {
  if container_exists "${CONTAINER_NAME}"; then
    if container_running "${CONTAINER_NAME}"; then
      backend_previous_was_running=true
      if ! docker stop "${CONTAINER_NAME}" >/dev/null; then
        if ! container_running "${CONTAINER_NAME}"; then
          if ! docker start "${CONTAINER_NAME}" >/dev/null || ! wait_for_url \
            "backend after aborted stop" \
            "${HEALTHCHECK_URL:-http://127.0.0.1:$(host_port "${HOST_PORT}")/api/v1/health}"; then
            backend_switch_abort_recovery_failed=true
          fi
        fi
        echo "could not stop current backend; switch aborted" >&2
        return 1
      fi
    fi
    if ! docker rename "${CONTAINER_NAME}" "${PREVIOUS_CONTAINER}"; then
      if [[ "${backend_previous_was_running}" == "true" ]]; then
        if ! docker start "${CONTAINER_NAME}" >/dev/null 2>&1 || ! wait_for_url \
          "backend after aborted switch" \
          "${HEALTHCHECK_URL:-http://127.0.0.1:$(host_port "${HOST_PORT}")/api/v1/health}"; then
          backend_switch_abort_recovery_failed=true
        fi
      fi
      echo "could not retain current backend as ${PREVIOUS_CONTAINER}; switch aborted" >&2
      return 1
    fi
    backend_previous_available=true
  fi
  backend_switched=true
  if ! docker run -d \
    --name "${CONTAINER_NAME}" \
    --restart unless-stopped \
    --network "${DOCKER_NETWORK}" \
    --env-file "${RUNTIME_ENV_FILE}" \
    -e PORT="${APP_PORT}" \
    -e AUTO_MIGRATE=false \
    -e SEED_DATA=false \
    -e RUN_BACKGROUND_JOBS=true \
    --label "${HEALTHCHECK_LABEL_KEY}=${BACKEND_HEALTHCHECK_REVISION}" \
    --health-cmd "${BACKEND_HEALTHCHECK_COMMAND}" \
    --health-interval 30s \
    --health-timeout 10s \
    --health-retries 3 \
    --health-start-period 30s \
    -p "${HOST_PORT}:${APP_PORT}" \
    "${target_image_id}" >/dev/null; then
    echo "replacement backend could not start" >&2
    return 1
  fi
  local backend_healthcheck_url
  backend_healthcheck_url="${HEALTHCHECK_URL:-http://127.0.0.1:$(host_port "${HOST_PORT}")/api/v1/health}"
  wait_for_url "replacement backend" "${backend_healthcheck_url}"
}

rollback_backend() {
  if [[ "${backend_switched}" != "true" ]]; then
    return 0
  fi
  docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
  if [[ "${backend_previous_available}" == "true" ]] && container_exists "${PREVIOUS_CONTAINER}"; then
    docker rename "${PREVIOUS_CONTAINER}" "${CONTAINER_NAME}" || return 1
    if [[ "${backend_previous_was_running}" == "true" ]]; then
      docker start "${CONTAINER_NAME}" >/dev/null || return 1
      if ! wait_for_url \
        "restored backend" \
        "${HEALTHCHECK_URL:-http://127.0.0.1:$(host_port "${HOST_PORT}")/api/v1/health}"; then
        append_release_metadata backend_rollback unhealthy
        return 1
      fi
    fi
  fi
  backend_switched=false
  append_release_metadata backend_rollback restored
}

on_deploy_exit() {
  local deploy_exit_code=$?
  local final_exit_code="${deploy_exit_code}"
  local backend_rollback_required="${backend_switched}"
  local caddy_rollback_required="${caddy_switched}"
  local postgres_health_rollback_required="${postgres_health_previous_available}"
  local minio_health_rollback_required="${minio_health_previous_available}"
  local backend_rollback_exit_code=0
  local caddy_rollback_exit_code=0
  local postgres_health_rollback_exit_code=0
  local minio_health_rollback_exit_code=0
  local backend_rollback_status=not-required
  local caddy_rollback_status=not-required
  local postgres_health_rollback_status=not-required
  local minio_health_rollback_status=not-required
  trap - EXIT
  cleanup_registry_auth
  cleanup_runtime_env
  if [[ "${deployment_succeeded}" != "true" ]]; then
    set +e
    docker rm -f "${CANDIDATE_CONTAINER}" >/dev/null 2>&1
    if [[ "${backend_switch_abort_recovery_failed}" == "true" ]]; then
      backend_rollback_exit_code=1
      backend_rollback_status=failed
    fi
    if [[ "${caddy_switch_abort_recovery_failed}" == "true" ]]; then
      caddy_rollback_exit_code=1
      caddy_rollback_status=failed
    fi
    if [[ "${postgres_health_rollback_required}" == "true" ]]; then
      restore_infrastructure_previous \
        "${POSTGRES_CONTAINER}" \
        "${POSTGRES_HEALTH_PREVIOUS_CONTAINER}" \
        "${postgres_health_previous_was_running}" \
        wait_for_postgres
      postgres_health_rollback_exit_code=$?
      if [[ "${postgres_health_rollback_exit_code}" -eq 0 ]]; then
        postgres_health_rollback_status=restored
        postgres_health_previous_available=false
      else
        postgres_health_rollback_status=failed
      fi
    fi
    if [[ "${minio_health_rollback_required}" == "true" ]]; then
      restore_infrastructure_previous \
        "${MINIO_CONTAINER}" \
        "${MINIO_HEALTH_PREVIOUS_CONTAINER}" \
        "${minio_health_previous_was_running}" \
        wait_for_minio
      minio_health_rollback_exit_code=$?
      if [[ "${minio_health_rollback_exit_code}" -eq 0 ]]; then
        minio_health_rollback_status=restored
        minio_health_previous_available=false
      else
        minio_health_rollback_status=failed
      fi
    fi
    if [[ "${backend_rollback_required}" == "true" ]]; then
      rollback_backend
      backend_rollback_exit_code=$?
      if [[ "${backend_rollback_exit_code}" -eq 0 ]]; then
        backend_rollback_status=restored
      else
        backend_rollback_status=failed
      fi
    fi
    if [[ "${caddy_rollback_required}" == "true" ]]; then
      rollback_caddy
      caddy_rollback_exit_code=$?
      if [[ "${caddy_rollback_exit_code}" -eq 0 ]]; then
        caddy_rollback_status=restored
      else
        caddy_rollback_status=failed
      fi
    fi
    append_release_metadata deploy_exit_code "${deploy_exit_code}"
    append_release_metadata backend_rollback_status "${backend_rollback_status}"
    append_release_metadata backend_rollback_exit_code "${backend_rollback_exit_code}"
    append_release_metadata caddy_rollback_status "${caddy_rollback_status}"
    append_release_metadata caddy_rollback_exit_code "${caddy_rollback_exit_code}"
    append_release_metadata postgres_health_rollback_status "${postgres_health_rollback_status}"
    append_release_metadata postgres_health_rollback_exit_code "${postgres_health_rollback_exit_code}"
    append_release_metadata minio_health_rollback_status "${minio_health_rollback_status}"
    append_release_metadata minio_health_rollback_exit_code "${minio_health_rollback_exit_code}"
    if [[ "${backend_rollback_exit_code}" -ne 0 || "${caddy_rollback_exit_code}" -ne 0 || \
      "${postgres_health_rollback_exit_code}" -ne 0 || "${minio_health_rollback_exit_code}" -ne 0 ]]; then
      append_release_metadata rollback_status failed
      final_exit_code=90
      write_release_result rollback_failed "${final_exit_code}"
      echo "CRITICAL: deploy failed and rollback did not complete (backend=${backend_rollback_status}, caddy=${caddy_rollback_status}, postgres-health=${postgres_health_rollback_status}, minio-health=${minio_health_rollback_status})" >&2
    else
      if [[ "${backend_rollback_required}" == "true" || "${caddy_rollback_required}" == "true" || \
        "${postgres_health_rollback_required}" == "true" || "${minio_health_rollback_required}" == "true" ]]; then
        append_release_metadata rollback_status restored
      else
        append_release_metadata rollback_status not-required
      fi
      write_release_result failed "${deploy_exit_code}"
    fi
    if [[ -n "${predeploy_backup_path}" && -f "${predeploy_backup_path}" ]]; then
      echo "deploy failed; predeploy database dump retained at ${predeploy_backup_path}" >&2
      if [[ "${migration_completed}" == "true" ]]; then
        echo "migration contract completed at version ${migration_version_after}; the later deploy stage failed" >&2
        echo "container rollback is safe only for backward-compatible schema changes; never auto-restore while either application version may have accepted writes" >&2
      elif [[ "${migration_up_started}" == "true" ]]; then
        echo "forward migrations may have committed; inspect the retained migration output and ledger before retrying" >&2
        echo "container rollback is safe only for backward-compatible schema changes; never auto-restore while either application version may have accepted writes" >&2
      else
        echo "scc-migrate up was not started; inspect the retained validation/check/status output before retrying" >&2
      fi
    fi
  fi
  exit "${final_exit_code}"
}

stop_nginx_for_caddy() {
  if [[ "${DISABLE_NGINX}" != "true" ]]; then
    return 0
  fi
  if command -v systemctl >/dev/null 2>&1; then
    sudo -n systemctl disable --now nginx >/dev/null 2>&1 || true
  fi
  sudo -n pkill -x nginx >/dev/null 2>&1 || true
}

ensure_caddy() {
  if [[ "${MANAGE_CADDY}" != "true" ]]; then
    return 0
  fi
  stop_nginx_for_caddy
  docker volume create "${CADDY_DATA_VOLUME}" >/dev/null
  docker volume create "${CADDY_CONFIG_VOLUME}" >/dev/null

  if [[ "${CADDYFILE_PATH}" != /* ]]; then
    echo "CADDYFILE_PATH must be an absolute path: ${CADDYFILE_PATH}" >&2
    return 1
  fi

  local caddyfile_dir caddyfile_tmp
  caddyfile_dir="$(dirname -- "${CADDYFILE_PATH}")"
  mkdir -p "${caddyfile_dir}"
  caddyfile_tmp="$(mktemp "${caddyfile_dir}/.Caddyfile.tmp.XXXXXX")"
  cleanup_caddyfile_tmp() {
    rm -f -- "${caddyfile_tmp}"
    trap - RETURN
  }
  trap cleanup_caddyfile_tmp RETURN

  cat >"${caddyfile_tmp}" <<EOF
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
  chmod 0644 "${caddyfile_tmp}"

  if ! docker run --rm \
    --network "${DOCKER_NETWORK}" \
    -v "${caddyfile_tmp}:/etc/caddy/Caddyfile:ro" \
    --entrypoint caddy \
    "${CADDY_IMAGE_ID}" \
    validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null; then
    echo "generated Caddyfile failed validation; keeping ${CADDYFILE_PATH} unchanged" >&2
    return 1
  fi

  if [[ -f "${CADDYFILE_PATH}" ]]; then
    caddyfile_previous_state=present
    caddyfile_previous_path="${release_dir}/Caddyfile.previous"
    cp -p "${CADDYFILE_PATH}" "${caddyfile_previous_path}"
    chmod 0600 "${caddyfile_previous_path}"
  else
    caddyfile_previous_state=absent
  fi

  if container_exists "${CADDY_CONTAINER}"; then
    if container_running "${CADDY_CONTAINER}"; then
      caddy_previous_was_running=true
      if ! docker stop "${CADDY_CONTAINER}" >/dev/null; then
        if ! container_running "${CADDY_CONTAINER}"; then
          if ! docker start "${CADDY_CONTAINER}" >/dev/null || ! wait_for_container_running "Caddy after aborted stop" "${CADDY_CONTAINER}"; then
            caddy_switch_abort_recovery_failed=true
          fi
        fi
        echo "could not stop current Caddy; switch aborted" >&2
        return 1
      fi
    fi
    if ! docker rename "${CADDY_CONTAINER}" "${CADDY_PREVIOUS_CONTAINER}"; then
      if [[ "${caddy_previous_was_running}" == "true" ]]; then
        if ! docker start "${CADDY_CONTAINER}" >/dev/null 2>&1 || ! wait_for_container_running "Caddy after aborted switch" "${CADDY_CONTAINER}"; then
          caddy_switch_abort_recovery_failed=true
        fi
      fi
      echo "could not retain current Caddy as ${CADDY_PREVIOUS_CONTAINER}; switch aborted" >&2
      return 1
    fi
    caddy_previous_available=true
  fi
  caddy_switched=true
  mv -f -- "${caddyfile_tmp}" "${CADDYFILE_PATH}"

  if ! docker run -d \
    --name "${CADDY_CONTAINER}" \
    --restart unless-stopped \
    --network "${DOCKER_NETWORK}" \
    --label "${HEALTHCHECK_LABEL_KEY}=${CADDY_HEALTHCHECK_REVISION}" \
    --health-cmd "${CADDY_HEALTHCHECK_COMMAND}" \
    --health-interval 30s \
    --health-timeout 10s \
    --health-retries 3 \
    --health-start-period 20s \
    -p 80:80 \
    -p 443:443 \
    -v "${CADDYFILE_PATH}:/etc/caddy/Caddyfile:ro" \
    -v "${CADDY_DATA_VOLUME}:/data" \
    -v "${CADDY_CONFIG_VOLUME}:/config" \
    "${CADDY_IMAGE_ID}" >/dev/null; then
    echo "replacement Caddy could not start" >&2
    return 1
  fi
  wait_for_container_running "replacement Caddy" "${CADDY_CONTAINER}"
}

wait_for_container_running() {
  local label="$1" container="$2" attempts="${3:-15}"
  for _ in $(seq 1 "${attempts}"); do
    if container_running "${container}"; then
      return 0
    fi
    sleep 1
  done
  echo "${label} did not remain running" >&2
  return 1
}

rollback_caddy() {
  if [[ "${caddy_switched}" != "true" ]]; then
    return 0
  fi
  docker rm -f "${CADDY_CONTAINER}" >/dev/null 2>&1 || true
  if [[ "${caddyfile_previous_state}" == "present" && -f "${caddyfile_previous_path}" ]]; then
    local restore_tmp
    restore_tmp="$(mktemp "$(dirname -- "${CADDYFILE_PATH}")/.Caddyfile.rollback.XXXXXX")"
    cp "${caddyfile_previous_path}" "${restore_tmp}"
    chmod 0644 "${restore_tmp}"
    mv -f -- "${restore_tmp}" "${CADDYFILE_PATH}"
  else
    rm -f -- "${CADDYFILE_PATH}"
  fi
  if [[ "${caddy_previous_available}" == "true" ]] && container_exists "${CADDY_PREVIOUS_CONTAINER}"; then
    docker rename "${CADDY_PREVIOUS_CONTAINER}" "${CADDY_CONTAINER}" || return 1
    if [[ "${caddy_previous_was_running}" == "true" ]]; then
      docker start "${CADDY_CONTAINER}" >/dev/null || return 1
      wait_for_container_running "restored Caddy" "${CADDY_CONTAINER}" || return 1
    fi
  fi
  caddy_switched=false
  append_release_metadata caddy_rollback restored
}

verify_public_routes() {
  if [[ -n "${PUBLIC_API_HEALTHCHECK_URL:-}" ]]; then
    wait_for_url "public API" "${PUBLIC_API_HEALTHCHECK_URL}"
    public_health_checked=true
  fi
  if [[ -n "${PUBLIC_STORAGE_HEALTHCHECK_URL:-}" ]]; then
    wait_for_url "public storage" "${PUBLIC_STORAGE_HEALTHCHECK_URL}"
    public_health_checked=true
  fi
  if [[ -n "${FRONTEND_HEALTHCHECK_URL:-}" ]]; then
    wait_for_url "frontend" "${FRONTEND_HEALTHCHECK_URL}"
    public_health_checked=true
  fi

  if [[ "${MANAGE_CADDY}" == "true" && "${VERIFY_CADDY_RESTART}" == "true" ]]; then
    if [[ ! -f "${CADDYFILE_PATH}" ]]; then
      echo "persistent Caddyfile disappeared before restart verification" >&2
      return 1
    fi
    docker restart "${CADDY_CONTAINER}" >/dev/null
    wait_for_container_running "restarted Caddy" "${CADDY_CONTAINER}"
    if [[ -n "${PUBLIC_API_HEALTHCHECK_URL:-}" ]]; then
      wait_for_url "public API after Caddy restart" "${PUBLIC_API_HEALTHCHECK_URL}"
    fi
    if [[ -n "${PUBLIC_STORAGE_HEALTHCHECK_URL:-}" ]]; then
      wait_for_url "public storage after Caddy restart" "${PUBLIC_STORAGE_HEALTHCHECK_URL}"
    fi
    append_release_metadata caddy_restart_health passed
  fi
}

mark_release_current() {
  local current_tmp
  current_tmp="$(mktemp "${RELEASES_DIR}/.current.tmp.XXXXXX")"
  printf '%s\n' "${release_dir}" >"${current_tmp}"
  chmod 0600 "${current_tmp}"
  mv -f -- "${current_tmp}" "${RELEASES_DIR}/current"
}

finalize_deploy() {
  append_release_metadata final_internal_health passed
  if [[ "${public_health_checked}" == "true" ]]; then
    append_release_metadata public_health passed
  else
    append_release_metadata public_health not-configured
  fi
  write_release_result success 0
  mark_release_current
  deployment_succeeded=true
  backend_switched=false
  caddy_switched=false

  if [[ "${backend_previous_available}" == "true" ]]; then
    docker rm -f "${PREVIOUS_CONTAINER}" >/dev/null 2>&1 || echo "WARNING: could not remove ${PREVIOUS_CONTAINER}; reconcile it before the next deploy" >&2
  fi
  if [[ "${caddy_previous_available}" == "true" ]]; then
    docker rm -f "${CADDY_PREVIOUS_CONTAINER}" >/dev/null 2>&1 || echo "WARNING: could not remove ${CADDY_PREVIOUS_CONTAINER}; reconcile it before the next deploy" >&2
  fi
}

preflight_deploy
trap on_deploy_exit EXIT

prepare_runtime_env_file
prepare_target_image
prepare_infrastructure_images
ensure_network
ensure_postgres
ensure_minio
create_predeploy_backup
reconcile_infrastructure_healthchecks
run_migration_contract
configure_minio_bucket
run_backend_candidate
switch_backend
ensure_caddy
verify_public_routes
finalize_deploy
cleanup_runtime_env
trap - EXIT

echo "scc-backend is healthy"
echo "release: ${RELEASE_ID}"
echo "image: ${GHCR_IMAGE}"
echo "api: https://${API_HOST:-<managed-elsewhere>}/api/v1/health"
echo "storage: https://${STORAGE_HOST:-<managed-elsewhere>}"
echo "postgres container: ${POSTGRES_CONTAINER}"
echo "minio container: ${MINIO_CONTAINER}"
echo "caddy container: ${CADDY_CONTAINER}"
