#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

: "${APP_ENV_PATH:=/opt/scc-backend/.env}"
: "${CADDYFILE_PATH:=/opt/scc-backend/Caddyfile}"
: "${DEPLOY_STATE_DIR:=$(dirname -- "${APP_ENV_PATH}")/deploy}"
: "${RELEASES_DIR:=${DEPLOY_STATE_DIR}/releases}"
: "${CURRENT_RELEASE_PATH:=${RELEASES_DIR}/current}"
: "${BACKUP_ROOT:=/opt/scc-backend/backups}"
: "${DOCTOR_VERIFY_BACKUP_SCRIPT:=${script_dir}/verify-backup.sh}"
: "${DOCTOR_BACKUP_MAX_AGE_SECONDS:=93600}"
: "${DOCTOR_DISK_PATH:=/}"
: "${DOCTOR_DISK_WARN_PERCENT:=75}"
: "${DOCTOR_DISK_FAIL_PERCENT:=90}"
: "${DOCTOR_PROC_MEMINFO:=/proc/meminfo}"
: "${DOCTOR_RAM_WARN_KIB:=262144}"
: "${DOCTOR_RAM_FAIL_KIB:=131072}"
: "${DOCTOR_SWAP_WARN_KIB:=524288}"
: "${DOCTOR_REQUIRE_SWAP:=false}"
: "${DOCTOR_RESTART_FAIL_COUNT:=10}"
: "${DOCTOR_ENABLE_PUBLIC_CHECKS:=false}"
: "${DOCTOR_VERIFY_MINIO_CORS:=true}"
: "${DOCTOR_PUBLIC_TIMEOUT_SECONDS:=10}"
: "${DOCTOR_DOCKER_COMMAND:=docker}"
: "${DOCTOR_CURL_COMMAND:=curl}"
: "${DOCTOR_FS_ROOT:=}"

: "${CONTAINER_NAME:=scc-backend}"
: "${POSTGRES_CONTAINER:=scc-postgres}"
: "${MINIO_CONTAINER:=scc-minio}"
: "${CADDY_CONTAINER:=scc-caddy}"
: "${DOCKER_NETWORK:=scc-net}"
: "${POSTGRES_VOLUME:=scc-postgres-data}"
: "${MINIO_VOLUME:=scc-minio-data}"
: "${CADDY_DATA_VOLUME:=scc-caddy-data}"
: "${CADDY_CONFIG_VOLUME:=scc-caddy-config}"

pass_count=0
warn_count=0
fail_count=0
env_loaded=false
static_config_valid=true
BACKEND_IMAGE_ID=""
LOADED_ENV_KEYS=(__doctor_no_env_key__)

pass() {
  pass_count=$((pass_count + 1))
  printf 'PASS %-12s %s\n' "$1" "$2"
}

warn() {
  warn_count=$((warn_count + 1))
  printf 'WARN %-12s %s\n' "$1" "$2"
}

fail() {
  fail_count=$((fail_count + 1))
  printf 'FAIL %-12s %s\n' "$1" "$2"
}

config_fail() {
  static_config_valid=false
  fail config "$1"
}

has_control_character() {
  [[ "$1" == *[[:cntrl:]]* ]]
}

is_safe_identifier() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]
}

is_non_negative_integer() {
  [[ "$1" =~ ^[0-9]+$ ]]
}

is_boolean() {
  [[ "$1" == "true" || "$1" == "false" ]]
}

is_immutable_image_ref() {
  [[ "$1" =~ ^[^[:space:]@]+@sha256:[[:xdigit:]]{64}$ ]]
}

is_loopback_address() {
  [[ "$1" == "127.0.0.1" || "$1" == "::1" ]]
}

is_public_edge_address() {
  [[ -z "$1" || "$1" == "0.0.0.0" || "$1" == "::" ]]
}

is_valid_dns_host_port() {
  local input="$1" host port="" port_number label
  local labels=()
  if [[ "${input}" == *:* ]]; then
    host="${input%:*}"
    port="${input##*:}"
    [[ "${host}" != "${input}" && "${host}" != *:* ]] || return 1
    is_non_negative_integer "${port}" || return 1
    [[ "${#port}" -le 5 ]] || return 1
    port_number=$((10#${port}))
    (( port_number > 0 && port_number <= 65535 )) || return 1
  else
    host="${input}"
  fi
  [[ -n "${host}" && "${#host}" -le 253 && "${host}" != .* && "${host}" != *. && "${host}" != *..* ]] || return 1
  local IFS='.'
  read -r -a labels <<<"${host}"
  for label in "${labels[@]}"; do
    [[ "${label}" =~ ^([A-Za-z0-9]|[A-Za-z0-9][A-Za-z0-9-]{0,61}[A-Za-z0-9])$ ]] || return 1
  done
}

is_safe_cors_origin() {
  local origin="$1" host_port
  [[ "${origin}" == https://* ]] || return 1
  host_port="${origin#https://}"
  [[ "${host_port}" != */* && "${host_port}" != *\?* && "${host_port}" != *\#* && "${host_port}" != *@* ]] || return 1
  is_valid_dns_host_port "${host_port}"
}

is_persistent_path() {
  if [[ "$1" == *'/../'* || "$1" == */.. || "$1" == *'/./'* || "$1" == */. ]]; then
    return 1
  fi
  case "$1" in
    /tmp|/tmp/*|/private/tmp|/private/tmp/*|/var/tmp|/var/tmp/*|/run|/run/*|/dev/shm|/dev/shm/*)
      return 1
      ;;
    /*)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

physical_path() {
  if [[ -n "${DOCTOR_FS_ROOT}" ]]; then
    printf '%s%s\n' "${DOCTOR_FS_ROOT}" "$1"
  else
    printf '%s\n' "$1"
  fi
}

env_key_is_managed() {
  case "$1" in
    ENV|AUTO_MIGRATE|SEED_DATA|ENABLE_PHASE2_BORROWING|RUN_BACKGROUND_JOBS|PORT|CORS_ORIGINS|JWT_SECRET|JWT_ACCESS_TTL|JWT_REFRESH_TTL|DATABASE_URL|POSTGRES_DB|POSTGRES_USER|POSTGRES_PASSWORD|POSTGRES_IMAGE|MINIO_ROOT_USER|MINIO_ROOT_PASSWORD|MINIO_ACCESS_KEY|MINIO_SECRET_KEY|MINIO_BUCKET|MINIO_ENDPOINT|MINIO_PUBLIC_URL|MINIO_USE_SSL|MINIO_INTERNAL_ENDPOINT|MINIO_PUBLIC_ENDPOINT|MINIO_INTERNAL_USE_SSL|MINIO_PUBLIC_USE_SSL|MINIO_IMAGE|MINIO_MC_IMAGE|API_HOST|STORAGE_HOST|CADDY_EMAIL|CADDY_IMAGE)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

env_key_was_loaded() {
  local candidate
  for candidate in "${LOADED_ENV_KEYS[@]}"; do
    [[ "${candidate}" == "$1" ]] && return 0
  done
  return 1
}

clear_inherited_app_env() {
  local key
  for key in \
    ENV AUTO_MIGRATE SEED_DATA ENABLE_PHASE2_BORROWING RUN_BACKGROUND_JOBS \
    PORT CORS_ORIGINS JWT_SECRET JWT_ACCESS_TTL JWT_REFRESH_TTL \
    DATABASE_URL POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD POSTGRES_IMAGE \
    MINIO_ROOT_USER MINIO_ROOT_PASSWORD MINIO_ACCESS_KEY MINIO_SECRET_KEY MINIO_BUCKET \
    MINIO_ENDPOINT MINIO_PUBLIC_URL MINIO_USE_SSL MINIO_INTERNAL_ENDPOINT MINIO_PUBLIC_ENDPOINT \
    MINIO_INTERNAL_USE_SSL MINIO_PUBLIC_USE_SSL MINIO_IMAGE MINIO_MC_IMAGE \
    API_HOST STORAGE_HOST CADDY_EMAIL CADDY_IMAGE; do
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

load_app_env_safely() {
  local logical_path="${APP_ENV_PATH}" path mode permission_digits group_digit other_digit
  local raw line key value quote line_number=0 seen_key

  if [[ "${logical_path}" != /* ]] || has_control_character "${logical_path}"; then
    return 1
  fi
  path="$(physical_path "${logical_path}")"
  if [[ ! -f "${path}" || -L "${path}" || ! -r "${path}" ]]; then
    return 1
  fi
  if ! mode="$(file_mode "${path}")"; then
    return 1
  fi
  permission_digits="${mode: -3}"
  group_digit="${permission_digits:1:1}"
  other_digit="${permission_digits:2:1}"
  if [[ "${group_digit}" != "0" || "${other_digit}" != "0" ]]; then
    return 1
  fi

  while IFS= read -r raw || [[ -n "${raw}" ]]; do
    line_number=$((line_number + 1))
    quote=""
    if has_control_character "${raw}"; then
      return 1
    fi
    line="${raw#"${raw%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    [[ -z "${line}" || "${line}" == \#* ]] && continue
    if [[ "${line}" =~ ^export[[:space:]]+ ]]; then
      line="${line#export}"
      line="${line#"${line%%[![:space:]]*}"}"
    fi
    if [[ ! "${line}" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; then
      return 1
    fi
    key="${BASH_REMATCH[1]}"
    value="${BASH_REMATCH[2]}"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"

    for seen_key in "${LOADED_ENV_KEYS[@]}"; do
      [[ "${seen_key}" == "${key}" ]] && return 1
    done

    if [[ "${value}" == \'* || "${value}" == \"* ]]; then
      [[ "${#value}" -ge 2 ]] || return 1
      quote="${value:0:1}"
      [[ "${value: -1}" == "${quote}" ]] || return 1
      value="${value:1:${#value}-2}"
      [[ "${value}" != *"${quote}"* ]] || return 1
    elif [[ "${value}" == *[[:space:]]* ]]; then
      return 1
    fi
    has_control_character "${value}" && return 1
    if [[ "${quote}" == "\"" && ( "${value}" == *'$'* || "${value}" == *'`'* || "${value}" == *'\'* ) ]]; then
      return 1
    fi
    if [[ -z "${quote}" && ( "${value}" == *'$'* || "${value}" == *'`'* || "${value}" == *'\'* || "${value}" == *';'* || "${value}" == *'&'* || "${value}" == *'|'* || "${value}" == *'<'* || "${value}" == *'>'* || "${value}" == *'('* || "${value}" == *')'* ) ]]; then
      return 1
    fi

    if env_key_is_managed "${key}"; then
      LOADED_ENV_KEYS+=("${key}")
      printf -v "${key}" '%s' "${value}"
      export -n "${key}" 2>/dev/null || true
    fi
  done <"${path}"

  [[ "${line_number}" -gt 0 ]]
}

validate_static_configuration() {
  local value
  if [[ -n "${DOCTOR_FS_ROOT}" ]]; then
    if [[ "${DOCTOR_FS_ROOT}" != /* ]] || has_control_character "${DOCTOR_FS_ROOT}"; then
      config_fail "DOCTOR_FS_ROOT must be an absolute control-free test root"
    else
      DOCTOR_FS_ROOT="${DOCTOR_FS_ROOT%/}"
    fi
  fi
  for value in "${APP_ENV_PATH}" "${CADDYFILE_PATH}" "${RELEASES_DIR}" "${CURRENT_RELEASE_PATH}" "${BACKUP_ROOT}" "${DOCTOR_DISK_PATH}" "${DOCTOR_PROC_MEMINFO}"; do
    if [[ "${value}" != /* ]] || has_control_character "${value}"; then
      config_fail "host paths must be absolute and control-free"
      break
    fi
  done
  for value in "${CONTAINER_NAME}" "${POSTGRES_CONTAINER}" "${MINIO_CONTAINER}" "${CADDY_CONTAINER}" "${DOCKER_NETWORK}" "${POSTGRES_VOLUME}" "${MINIO_VOLUME}" "${CADDY_DATA_VOLUME}" "${CADDY_CONFIG_VOLUME}"; do
    if ! is_safe_identifier "${value}"; then
      config_fail "Docker resource names contain unsupported characters"
      break
    fi
  done
  for value in "${DOCTOR_BACKUP_MAX_AGE_SECONDS}" "${DOCTOR_DISK_WARN_PERCENT}" "${DOCTOR_DISK_FAIL_PERCENT}" "${DOCTOR_RAM_WARN_KIB}" "${DOCTOR_RAM_FAIL_KIB}" "${DOCTOR_SWAP_WARN_KIB}" "${DOCTOR_RESTART_FAIL_COUNT}" "${DOCTOR_PUBLIC_TIMEOUT_SECONDS}"; do
    if ! is_non_negative_integer "${value}"; then
      config_fail "numeric thresholds must be non-negative integers"
      break
    fi
  done
  if is_non_negative_integer "${DOCTOR_DISK_WARN_PERCENT}" && is_non_negative_integer "${DOCTOR_DISK_FAIL_PERCENT}"; then
    if (( DOCTOR_DISK_WARN_PERCENT >= DOCTOR_DISK_FAIL_PERCENT || DOCTOR_DISK_FAIL_PERCENT > 100 )); then
      config_fail "disk thresholds must satisfy warn < fail <= 100"
    fi
  fi
  if is_non_negative_integer "${DOCTOR_RAM_WARN_KIB}" && is_non_negative_integer "${DOCTOR_RAM_FAIL_KIB}"; then
    if (( DOCTOR_RAM_FAIL_KIB > DOCTOR_RAM_WARN_KIB )); then
      config_fail "RAM thresholds must satisfy fail <= warn"
    fi
  fi
  if ! is_boolean "${DOCTOR_REQUIRE_SWAP}" || ! is_boolean "${DOCTOR_ENABLE_PUBLIC_CHECKS}" || ! is_boolean "${DOCTOR_VERIFY_MINIO_CORS}"; then
    config_fail "boolean controls must be true or false"
  fi
}

check_environment() {
  local key origin cors_valid=true missing=() required_env cors_origins=()
  required_env=(
    ENV AUTO_MIGRATE SEED_DATA ENABLE_PHASE2_BORROWING RUN_BACKGROUND_JOBS
    CORS_ORIGINS DATABASE_URL JWT_SECRET
    POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD POSTGRES_IMAGE
    MINIO_ROOT_USER MINIO_ROOT_PASSWORD MINIO_ACCESS_KEY MINIO_SECRET_KEY MINIO_BUCKET
    MINIO_INTERNAL_ENDPOINT MINIO_PUBLIC_ENDPOINT MINIO_INTERNAL_USE_SSL MINIO_PUBLIC_USE_SSL
    MINIO_IMAGE MINIO_MC_IMAGE API_HOST STORAGE_HOST CADDY_IMAGE
  )
  for key in "${required_env[@]}"; do
    if ! env_key_was_loaded "${key}" || [[ -z "${!key:-}" ]]; then
      missing+=("${key}")
    fi
  done
  if (( ${#missing[@]} > 0 )); then
    fail env "missing required keys: ${missing[*]}"
    return 0
  fi
  pass env "required production keys are present (values redacted)"

  if [[ "${ENV}" != "production" || "${AUTO_MIGRATE}" != "false" || "${SEED_DATA}" != "false" ]]; then
    fail env "production safety flags are not locked down"
  else
    pass env "production migration and seed safety flags are locked down"
  fi
  if ! is_boolean "${ENABLE_PHASE2_BORROWING}" || ! is_boolean "${RUN_BACKGROUND_JOBS}"; then
    fail env "feature and background-job flags must be exactly true or false"
  elif [[ "${RUN_BACKGROUND_JOBS}" != "true" ]]; then
    fail env "active production backend must own background jobs"
  else
    pass env "active production backend is the background-job owner"
    pass env "Phase 2 borrowing flag is explicitly ${ENABLE_PHASE2_BORROWING}"
  fi
  if [[ "${MINIO_INTERNAL_USE_SSL}" != "false" || "${MINIO_PUBLIC_USE_SSL}" != "true" || "${MINIO_INTERNAL_ENDPOINT}" != "${MINIO_CONTAINER}:9000" || "${MINIO_PUBLIC_ENDPOINT}" != "${STORAGE_HOST}" ]] || ! is_valid_dns_host_port "${MINIO_PUBLIC_ENDPOINT}"; then
    fail env "MinIO internal/public endpoint policy is invalid"
  else
    pass env "MinIO internal/private and public/TLS roles are separated"
  fi
  IFS=',' read -r -a cors_origins <<<"${CORS_ORIGINS}"
  if (( ${#cors_origins[@]} == 0 )) || [[ "${CORS_ORIGINS}" == ,* || "${CORS_ORIGINS}" == *, || "${CORS_ORIGINS}" == *,,* ]]; then
    cors_valid=false
  fi
  for origin in "${cors_origins[@]}"; do
    if ! is_safe_cors_origin "${origin}"; then
      cors_valid=false
    fi
  done
  if [[ "${cors_valid}" != "true" ]]; then
    fail env "CORS_ORIGINS is unsafe or malformed for production"
  else
    pass env "CORS origins are explicit HTTPS origins"
  fi
  if [[ "${MINIO_ACCESS_KEY}" == "${MINIO_ROOT_USER}" && "${MINIO_SECRET_KEY}" == "${MINIO_ROOT_PASSWORD}" ]]; then
    warn env "application MinIO credentials are the root credential pair"
  else
    pass env "application MinIO credentials are separated from root credentials"
  fi
  if ! is_immutable_image_ref "${POSTGRES_IMAGE}" || ! is_immutable_image_ref "${MINIO_IMAGE}" || \
    ! is_immutable_image_ref "${MINIO_MC_IMAGE}" || ! is_immutable_image_ref "${CADDY_IMAGE}"; then
    fail env "infrastructure images must use immutable sha256 digest references"
  else
    pass env "infrastructure image references are immutable"
  fi
}

check_caddyfile() {
  local path
  if ! is_persistent_path "${CADDYFILE_PATH}"; then
    fail caddyfile "path must be absolute and outside temporary/runtime filesystems"
    return 0
  fi
  path="$(physical_path "${CADDYFILE_PATH}")"
  if [[ ! -f "${path}" || -L "${path}" || ! -r "${path}" ]]; then
    fail caddyfile "persistent regular file is missing or unreadable"
    return 0
  fi
  pass caddyfile "persistent configuration is readable"
}

docker_call() {
  "${DOCTOR_DOCKER_COMMAND}" "$@"
}

line_list_contains() {
  local haystack="$1" expected="$2" line
  while IFS= read -r line; do
    [[ "${line}" == "${expected}" ]] && return 0
  done <<<"${haystack}"
  return 1
}

inspect_container() {
  local name="$1" state running restarts health image_ref image_id networks expected_ref="" expected_id
  if ! docker_call inspect "${name}" >/dev/null 2>&1; then
    fail docker "expected container ${name} is missing"
    return 0
  fi
  if ! state="$(docker_call inspect -f '{{printf "%t|%d|" .State.Running .RestartCount}}{{if .State.Health}}{{printf "%s" .State.Health.Status}}{{else}}{{printf "none"}}{{end}}{{printf "|%s|%s" .Config.Image .Image}}' "${name}" 2>/dev/null)"; then
    fail docker "container ${name} state could not be inspected"
    return 0
  fi
  IFS='|' read -r running restarts health image_ref image_id <<<"${state}"
  if [[ "${running}" != "true" ]]; then
    fail docker "container ${name} is not running"
  elif [[ "${health}" == "unhealthy" ]]; then
    fail docker "container ${name} is unhealthy"
  elif [[ "${health}" == "starting" ]]; then
    warn docker "container ${name} health is still starting"
  elif [[ "${health}" == "none" ]]; then
    warn docker "container ${name} is running without a Docker healthcheck"
  elif [[ "${health}" == "healthy" ]]; then
    pass docker "container ${name} is running and ${health}"
  else
    fail docker "container ${name} health state is malformed"
  fi
  if ! is_non_negative_integer "${restarts}"; then
    fail docker "container ${name} restart count is unreadable"
  elif (( restarts >= DOCTOR_RESTART_FAIL_COUNT )); then
    fail docker "container ${name} restart count reached the critical threshold"
  elif (( restarts > 0 )); then
    warn docker "container ${name} has restarted ${restarts} time(s)"
  else
    pass docker "container ${name} has no recorded restarts"
  fi
  if has_control_character "${image_ref}" || has_control_character "${image_id}" || [[ -z "${image_id}" ]]; then
    fail docker "container ${name} image identity is malformed"
  fi
  if [[ "${name}" == "${CONTAINER_NAME}" ]]; then
    BACKEND_IMAGE_ID="${image_id}"
  elif [[ "${env_loaded}" == "true" ]]; then
    case "${name}" in
      "${POSTGRES_CONTAINER}") expected_ref="${POSTGRES_IMAGE}" ;;
      "${MINIO_CONTAINER}") expected_ref="${MINIO_IMAGE}" ;;
      "${CADDY_CONTAINER}") expected_ref="${CADDY_IMAGE}" ;;
    esac
    if [[ -n "${expected_ref}" ]]; then
      if ! expected_id="$(docker_call image inspect -f '{{.Id}}' "${expected_ref}" 2>/dev/null)" || [[ ! "${expected_id}" =~ ^sha256:[[:xdigit:]]{64}$ ]]; then
        fail docker "configured immutable image for ${name} is unavailable locally"
      elif [[ "${image_id}" != "${expected_id}" ]]; then
        fail docker "container ${name} does not run its configured immutable image"
      else
        pass docker "container ${name} matches its configured immutable image"
      fi
    fi
  fi

  if ! networks="$(docker_call inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{printf "%s\n" $name}}{{end}}' "${name}" 2>/dev/null)"; then
    fail docker "container ${name} networks could not be inspected"
  elif ! line_list_contains "${networks}" "${DOCKER_NETWORK}"; then
    fail docker "container ${name} is not attached to ${DOCKER_NETWORK}"
  else
    pass docker "container ${name} is attached to ${DOCKER_NETWORK}"
  fi
}

inspect_backend_runtime_env() {
  local output line key value
  local runtime_env="" auto_migrate="" seed_data="" phase2="" background_jobs=""
  local runtime_env_count=0 auto_migrate_count=0 seed_data_count=0 phase2_count=0 background_jobs_count=0
  if ! output="$(docker_call inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "${CONTAINER_NAME}" 2>/dev/null)"; then
    fail runtime "active backend environment could not be inspected"
    return 0
  fi
  while IFS= read -r line; do
    [[ "${line}" == *=* ]] || continue
    key="${line%%=*}"
    value="${line#*=}"
    case "${key}" in
      ENV) runtime_env="${value}"; runtime_env_count=$((runtime_env_count + 1)) ;;
      AUTO_MIGRATE) auto_migrate="${value}"; auto_migrate_count=$((auto_migrate_count + 1)) ;;
      SEED_DATA) seed_data="${value}"; seed_data_count=$((seed_data_count + 1)) ;;
      ENABLE_PHASE2_BORROWING) phase2="${value}"; phase2_count=$((phase2_count + 1)) ;;
      RUN_BACKGROUND_JOBS) background_jobs="${value}"; background_jobs_count=$((background_jobs_count + 1)) ;;
    esac
  done <<<"${output}"
  if [[ "${runtime_env_count}" -ne 1 || "${auto_migrate_count}" -ne 1 || "${seed_data_count}" -ne 1 || \
    "${phase2_count}" -ne 1 || "${background_jobs_count}" -ne 1 ]]; then
    fail runtime "active backend must contain exactly one value for every production runtime flag"
  elif [[ "${runtime_env}" != "production" || "${auto_migrate}" != "false" || "${seed_data}" != "false" || \
    "${phase2}" != "${ENABLE_PHASE2_BORROWING}" || "${background_jobs}" != "true" ]]; then
    fail runtime "active backend flags do not match the protected production environment"
  else
    pass runtime "active backend flags match production and single-job-owner policy"
  fi
}

inspect_ephemeral_image() {
  local label="$1" image_ref="$2" image_id
  if ! image_id="$(docker_call image inspect -f '{{.Id}}' "${image_ref}" 2>/dev/null)" || [[ ! "${image_id}" =~ ^sha256:[[:xdigit:]]{64}$ ]]; then
    fail docker "configured ${label} image is unavailable locally"
  else
    pass docker "configured ${label} image is available by immutable identity"
  fi
}

inspect_ports() {
  local name="$1" role="$2" output line container_port host_ip host_port
  local saw_api=false saw_console=false saw_http=false saw_https=false issue=false
  if ! output="$(docker_call inspect -f '{{range $port, $bindings := .NetworkSettings.Ports}}{{range $bindings}}{{printf "%s|%s|%s\n" $port .HostIp .HostPort}}{{end}}{{end}}' "${name}" 2>/dev/null)"; then
    fail ports "container ${name} port bindings could not be inspected"
    return 0
  fi
  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    IFS='|' read -r container_port host_ip host_port <<<"${line}"
    if has_control_character "${container_port}${host_ip}${host_port}"; then
      issue=true
      continue
    fi
    case "${role}" in
      backend)
        [[ "${container_port}" == "8080/tcp" ]] && saw_api=true
        if ! is_loopback_address "${host_ip}"; then issue=true; fi
        ;;
      postgres)
        issue=true
        ;;
      minio)
        [[ "${container_port}" == "9000/tcp" ]] && saw_api=true
        [[ "${container_port}" == "9001/tcp" ]] && saw_console=true
        if ! is_loopback_address "${host_ip}"; then issue=true; fi
        ;;
      edge)
        if [[ "${container_port}" == "80/tcp" && "${host_port}" == "80" ]] && is_public_edge_address "${host_ip}"; then saw_http=true; fi
        if [[ "${container_port}" == "443/tcp" && "${host_port}" == "443" ]] && is_public_edge_address "${host_ip}"; then saw_https=true; fi
        ;;
    esac
  done <<<"${output}"

  case "${role}" in
    backend)
      if [[ "${issue}" == "true" || "${saw_api}" != "true" ]]; then
        fail ports "backend must publish 8080 only on loopback"
      else
        pass ports "backend host binding is loopback-only"
      fi
      ;;
    postgres)
      if [[ "${issue}" == "true" ]]; then fail ports "PostgreSQL must not publish a host port"; else pass ports "PostgreSQL has no host binding"; fi
      ;;
    minio)
      if [[ "${issue}" == "true" || "${saw_api}" != "true" || "${saw_console}" != "true" ]]; then
        fail ports "MinIO API and console must publish only on loopback"
      else
        pass ports "MinIO host bindings are loopback-only"
      fi
      ;;
    edge)
      if [[ "${saw_http}" != "true" || "${saw_https}" != "true" ]]; then
        fail ports "Caddy must expose public host ports 80 and 443"
      else
        pass ports "Caddy owns public edge ports 80 and 443"
      fi
      ;;
  esac
}

inspect_mounts() {
  local name="$1" role="$2" output line type volume_name source destination writable
  local saw_primary=false saw_secondary=false saw_caddyfile=false
  if ! output="$(docker_call inspect -f '{{range .Mounts}}{{printf "%s|%s|%s|%s|%t\n" .Type .Name .Source .Destination .RW}}{{end}}' "${name}" 2>/dev/null)"; then
    fail volumes "container ${name} mounts could not be inspected"
    return 0
  fi
  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue
    IFS='|' read -r type volume_name source destination writable <<<"${line}"
    case "${role}" in
      postgres)
        [[ "${type}" == "volume" && "${volume_name}" == "${POSTGRES_VOLUME}" && "${destination}" == "/var/lib/postgresql/data" && "${writable}" == "true" ]] && saw_primary=true
        ;;
      minio)
        [[ "${type}" == "volume" && "${volume_name}" == "${MINIO_VOLUME}" && "${destination}" == "/data" && "${writable}" == "true" ]] && saw_primary=true
        ;;
      edge)
        [[ "${type}" == "volume" && "${volume_name}" == "${CADDY_DATA_VOLUME}" && "${destination}" == "/data" ]] && saw_primary=true
        [[ "${type}" == "volume" && "${volume_name}" == "${CADDY_CONFIG_VOLUME}" && "${destination}" == "/config" ]] && saw_secondary=true
        [[ "${type}" == "bind" && "${source}" == "${CADDYFILE_PATH}" && "${destination}" == "/etc/caddy/Caddyfile" && "${writable}" == "false" ]] && saw_caddyfile=true
        ;;
    esac
  done <<<"${output}"
  case "${role}" in
    postgres|minio)
      if [[ "${saw_primary}" == "true" ]]; then pass volumes "container ${name} uses its expected writable named volume"; else fail volumes "container ${name} is missing its expected writable named volume mount"; fi
      ;;
    edge)
      if [[ "${saw_primary}" == "true" && "${saw_secondary}" == "true" && "${saw_caddyfile}" == "true" ]]; then
        pass volumes "Caddy data/config volumes and read-only host config are attached"
      else
        fail volumes "Caddy persistent mounts are incomplete or writable"
      fi
      ;;
  esac
}

check_docker() {
  local resource
  if ! command -v "${DOCTOR_DOCKER_COMMAND}" >/dev/null 2>&1; then
    fail docker "Docker command is unavailable"
    return 0
  fi
  if ! docker_call info >/dev/null 2>&1; then
    fail docker "Docker daemon is unavailable or inaccessible"
    return 0
  fi
  pass docker "daemon is available"
  if ! docker_call network inspect "${DOCKER_NETWORK}" >/dev/null 2>&1; then
    fail docker "expected network ${DOCKER_NETWORK} is missing"
  else
    pass docker "expected network exists"
  fi
  for resource in "${POSTGRES_VOLUME}" "${MINIO_VOLUME}" "${CADDY_DATA_VOLUME}" "${CADDY_CONFIG_VOLUME}"; do
    if ! docker_call volume inspect "${resource}" >/dev/null 2>&1; then
      fail volumes "expected volume ${resource} is missing"
    else
      pass volumes "expected volume ${resource} exists"
    fi
  done

  inspect_container "${CONTAINER_NAME}"
  inspect_container "${POSTGRES_CONTAINER}"
  inspect_container "${MINIO_CONTAINER}"
  inspect_container "${CADDY_CONTAINER}"
  if [[ "${env_loaded}" == "true" ]]; then
    inspect_ephemeral_image "MinIO client" "${MINIO_MC_IMAGE}"
    inspect_backend_runtime_env
  fi
  inspect_ports "${CONTAINER_NAME}" backend
  inspect_ports "${POSTGRES_CONTAINER}" postgres
  inspect_ports "${MINIO_CONTAINER}" minio
  inspect_ports "${CADDY_CONTAINER}" edge
  inspect_mounts "${POSTGRES_CONTAINER}" postgres
  inspect_mounts "${MINIO_CONTAINER}" minio
  inspect_mounts "${CADDY_CONTAINER}" edge
}

check_disk() {
  local path output usage
  path="$(physical_path "${DOCTOR_DISK_PATH}")"
  if ! output="$(df -Pk "${path}" 2>/dev/null)"; then
    fail capacity "disk usage could not be inspected"
    return 0
  fi
  usage="$(printf '%s\n' "${output}" | awk 'NR == 2 {gsub(/%/, "", $5); print $5}')"
  if ! is_non_negative_integer "${usage}" || (( usage > 100 )); then
    fail capacity "disk usage output is malformed"
  elif (( usage >= DOCTOR_DISK_FAIL_PERCENT )); then
    fail capacity "disk usage is ${usage}% (critical)"
  elif (( usage >= DOCTOR_DISK_WARN_PERCENT )); then
    warn capacity "disk usage is ${usage}% (warning)"
  else
    pass capacity "disk usage is ${usage}%"
  fi
}

check_memory() {
  local path available swap_total mem_free buffers cached
  path="$(physical_path "${DOCTOR_PROC_MEMINFO}")"
  if [[ ! -r "${path}" ]]; then
    fail capacity "memory information is unavailable"
    return 0
  fi
  available="$(awk '/^MemAvailable:/ {print $2; exit}' "${path}")"
  if ! is_non_negative_integer "${available}"; then
    mem_free="$(awk '/^MemFree:/ {print $2; exit}' "${path}")"
    buffers="$(awk '/^Buffers:/ {print $2; exit}' "${path}")"
    cached="$(awk '/^Cached:/ {print $2; exit}' "${path}")"
    if is_non_negative_integer "${mem_free}" && is_non_negative_integer "${buffers}" && is_non_negative_integer "${cached}"; then
      available=$((mem_free + buffers + cached))
    fi
  fi
  swap_total="$(awk '/^SwapTotal:/ {print $2; exit}' "${path}")"
  if ! is_non_negative_integer "${available}" || ! is_non_negative_integer "${swap_total}"; then
    fail capacity "memory information is malformed"
    return 0
  fi
  if (( available < DOCTOR_RAM_FAIL_KIB )); then
    fail capacity "available RAM is below the critical threshold"
  elif (( available < DOCTOR_RAM_WARN_KIB )); then
    warn capacity "available RAM is below the warning threshold"
  else
    pass capacity "available RAM is above the warning threshold"
  fi
  if (( swap_total < DOCTOR_SWAP_WARN_KIB )); then
    if [[ "${DOCTOR_REQUIRE_SWAP}" == "true" ]]; then
      fail capacity "swap is below the required threshold"
    else
      warn capacity "swap is below the recommended threshold"
    fi
  else
    pass capacity "swap meets the recommended threshold"
  fi
}

read_kv_value() {
  local file="$1" wanted="$2" line key value found=false
  KV_VALUE=""
  while IFS= read -r line || [[ -n "${line}" ]]; do
    if has_control_character "${line}" || [[ ! "${line}" =~ ^([a-z0-9_]+)=(.*)$ ]]; then
      return 1
    fi
    key="${BASH_REMATCH[1]}"
    value="${BASH_REMATCH[2]}"
    if [[ "${key}" == "${wanted}" ]]; then
      [[ "${found}" == "false" ]] || return 1
      found=true
      KV_VALUE="${value}"
    fi
  done <"${file}"
  [[ "${found}" == "true" ]]
}

check_release() {
  local current_file active_release="" pointer_line release_suffix release_dir release_env result_env pointer_lines=0
  local release_id source_commit target_image_ref target_image_id result exit_code
  current_file="$(physical_path "${CURRENT_RELEASE_PATH}")"
  if [[ ! -f "${current_file}" || -L "${current_file}" || ! -r "${current_file}" ]]; then
    fail release "active release pointer is missing or unreadable"
    return 0
  fi
  while IFS= read -r pointer_line || [[ -n "${pointer_line}" ]]; do
    pointer_lines=$((pointer_lines + 1))
    [[ "${pointer_lines}" -eq 1 ]] && active_release="${pointer_line}"
  done <"${current_file}"
  release_suffix="${active_release#"${RELEASES_DIR}/"}"
  if [[ "${pointer_lines}" -ne 1 || -z "${active_release}" || "${active_release}" != "${RELEASES_DIR}/"* || "${release_suffix}" == */* || "${release_suffix}" == "." || "${release_suffix}" == ".." || ! "${release_suffix}" =~ ^[A-Za-z0-9._-]+$ ]] || has_control_character "${active_release}"; then
    fail release "active release pointer is malformed or outside the release directory"
    return 0
  fi
  release_dir="$(physical_path "${active_release}")"
  release_env="${release_dir}/release.env"
  result_env="${release_dir}/result.env"
  if [[ ! -d "${release_dir}" || -L "${release_dir}" || ! -f "${release_env}" || -L "${release_env}" || ! -r "${release_env}" || ! -f "${result_env}" || -L "${result_env}" || ! -r "${result_env}" ]]; then
    fail release "active release metadata or result is missing"
    return 0
  fi
  if ! read_kv_value "${release_env}" release_id; then fail release "release metadata is malformed"; return 0; fi
  release_id="${KV_VALUE}"
  if ! read_kv_value "${release_env}" source_commit; then fail release "release metadata is incomplete"; return 0; fi
  source_commit="${KV_VALUE}"
  if ! read_kv_value "${release_env}" target_image_ref; then fail release "release metadata is incomplete"; return 0; fi
  target_image_ref="${KV_VALUE}"
  if ! read_kv_value "${release_env}" target_image_id; then fail release "release metadata is incomplete"; return 0; fi
  target_image_id="${KV_VALUE}"
  if ! read_kv_value "${result_env}" result; then fail release "release result is malformed"; return 0; fi
  result="${KV_VALUE}"
  if ! read_kv_value "${result_env}" exit_code; then fail release "release result is incomplete"; return 0; fi
  exit_code="${KV_VALUE}"

  if [[ ! "${release_id}" =~ ^[A-Za-z0-9._-]+$ || "${release_id}" != "${active_release##*/}" || ! "${source_commit}" =~ ^[A-Za-z0-9._-]+$ ]]; then
    fail release "release identity metadata is invalid"
  elif [[ ! "${target_image_ref}" =~ @sha256:[a-fA-F0-9]{64}$ || ! "${target_image_id}" =~ ^sha256:[a-fA-F0-9]{64}$ ]]; then
    fail release "active release does not record immutable image digests"
  elif [[ "${result}" != "success" || "${exit_code}" != "0" ]]; then
    fail release "active release is not recorded as successful"
  elif [[ -n "${BACKEND_IMAGE_ID}" && "${BACKEND_IMAGE_ID}" != "${target_image_id}" ]]; then
    fail release "running backend image does not match active release metadata"
  else
    pass release "active successful release matches an immutable backend image"
  fi
}

check_backup() {
  local backup_path
  if [[ ! -f "${DOCTOR_VERIFY_BACKUP_SCRIPT}" || ! -x "${DOCTOR_VERIFY_BACKUP_SCRIPT}" ]]; then
    warn backup "verification script is not installed; freshness was not checked"
    return 0
  fi
  backup_path="$(physical_path "${BACKUP_ROOT}")"
  if BACKUP_ROOT="${backup_path}" "${DOCTOR_VERIFY_BACKUP_SCRIPT}" --latest --max-age-seconds "${DOCTOR_BACKUP_MAX_AGE_SECONDS}" --freshness-only >/dev/null 2>&1; then
    pass backup "latest completed backup passes the freshness gate"
  else
    fail backup "latest completed backup is missing, stale, or invalid"
  fi
}

is_safe_public_url() {
  local url="$1" remainder authority path
  [[ "${url}" == https://* ]] || return 1
  has_control_character "${url}" && return 1
  remainder="${url#https://}"
  authority="${remainder%%/*}"
  if [[ "${remainder}" == */* ]]; then
    path="/${remainder#*/}"
  else
    path=""
  fi
  [[ -z "${path}" || "${path}" == /* ]] || return 1
  [[ "${url}" != *\?* && "${url}" != *\#* && "${authority}" != *@* ]] || return 1
  is_valid_dns_host_port "${authority}"
}

check_public_url() {
  local label="$1" url="$2"
  if ! is_safe_public_url "${url}"; then
    fail public "${label} URL is missing or unsafe"
  elif "${DOCTOR_CURL_COMMAND}" --fail --silent --show-error --max-time "${DOCTOR_PUBLIC_TIMEOUT_SECONDS}" "${url}" >/dev/null 2>&1; then
    pass public "${label} TLS endpoint is reachable"
  else
    fail public "${label} TLS endpoint failed"
  fi
}

run_minio_cors_preflight() {
  local origin="$1" url="$2" output
  if ! output="$("${DOCTOR_CURL_COMMAND}" \
    --silent \
    --show-error \
    --max-time "${DOCTOR_PUBLIC_TIMEOUT_SECONDS}" \
    --request OPTIONS \
    --header "Origin: ${origin}" \
    --header 'Access-Control-Request-Method: PUT' \
    --header 'Access-Control-Request-Headers: content-type,if-none-match' \
    --dump-header - \
    --output /dev/null \
    --write-out $'\nSCC_HTTP_STATUS=%{http_code}\n' \
    "${url}" 2>/dev/null)"; then
    return 1
  fi
  printf '%s\n' "${output}"
}

parse_minio_cors_preflight() {
  local response="$1" line lower value
  MINIO_PREFLIGHT_STATUS=''
  MINIO_PREFLIGHT_ALLOW_ORIGIN=''
  MINIO_PREFLIGHT_ALLOW_ORIGIN_COUNT=0
  while IFS= read -r line; do
    line="${line%$'\r'}"
    case "${line}" in
      SCC_HTTP_STATUS=*)
        MINIO_PREFLIGHT_STATUS="${line#SCC_HTTP_STATUS=}"
        ;;
      *)
        lower="$(printf '%s' "${line}" | tr '[:upper:]' '[:lower:]')"
        if [[ "${lower}" == access-control-allow-origin:* ]]; then
          value="${line#*:}"
          value="${value#"${value%%[![:space:]]*}"}"
          value="${value%"${value##*[![:space:]]}"}"
          MINIO_PREFLIGHT_ALLOW_ORIGIN="${value}"
          MINIO_PREFLIGHT_ALLOW_ORIGIN_COUNT=$((MINIO_PREFLIGHT_ALLOW_ORIGIN_COUNT + 1))
        fi
        ;;
    esac
  done <<<"${response}"
}

check_minio_cors_preflight() {
  local origin response probe_origin probe_url configured_origins=()
  if [[ "${DOCTOR_VERIFY_MINIO_CORS}" != "true" ]]; then
    warn cors "MinIO public CORS verification is disabled"
    return 0
  fi
  if [[ "${env_loaded}" != "true" || -z "${MINIO_PUBLIC_ENDPOINT:-}" || -z "${MINIO_BUCKET:-}" || -z "${CORS_ORIGINS:-}" ]]; then
    fail cors "MinIO public CORS verification lacks protected environment context"
    return 0
  fi
  probe_url="https://${MINIO_PUBLIC_ENDPOINT}/${MINIO_BUCKET}/__scc_cors_probe__"
  if ! is_safe_public_url "${probe_url}"; then
    fail cors "MinIO public CORS probe URL is unsafe"
    return 0
  fi
  IFS=',' read -r -a configured_origins <<<"${CORS_ORIGINS}"
  for origin in "${configured_origins[@]}"; do
    if ! response="$(run_minio_cors_preflight "${origin}" "${probe_url}")"; then
      fail cors "MinIO preflight failed for a configured origin"
      continue
    fi
    parse_minio_cors_preflight "${response}"
    if [[ ! "${MINIO_PREFLIGHT_STATUS}" =~ ^2[0-9][0-9]$ ]]; then
      fail cors "MinIO rejected a configured-origin preflight"
    elif [[ "${MINIO_PREFLIGHT_ALLOW_ORIGIN_COUNT}" -ne 1 || "${MINIO_PREFLIGHT_ALLOW_ORIGIN}" != "${origin}" ]]; then
      fail cors "MinIO CORS is wildcarded or drifted for a configured origin"
    else
      pass cors "MinIO allows one configured origin without wildcarding"
    fi
  done

  probe_origin='https://scc-cors-audit.invalid'
  for origin in "${configured_origins[@]}"; do
    if [[ "${origin}" == "${probe_origin}" ]]; then
      probe_origin='https://scc-cors-audit-two.invalid'
    fi
  done
  if ! response="$(run_minio_cors_preflight "${probe_origin}" "${probe_url}")"; then
    fail cors "MinIO unlisted-origin preflight could not be inspected"
    return 0
  fi
  parse_minio_cors_preflight "${response}"
  if (( MINIO_PREFLIGHT_ALLOW_ORIGIN_COUNT > 0 )); then
    fail cors "MinIO CORS accepts an unlisted origin (wildcard or policy drift)"
  else
    pass cors "MinIO rejects an unlisted origin"
  fi
}

check_public_endpoints() {
  if [[ "${DOCTOR_ENABLE_PUBLIC_CHECKS}" != "true" ]]; then
    warn public "TLS/public checks are disabled (no network request made)"
    return 0
  fi
  if ! command -v "${DOCTOR_CURL_COMMAND}" >/dev/null 2>&1; then
    fail public "curl command is unavailable"
    return 0
  fi
  check_public_url "API" "${PUBLIC_API_HEALTHCHECK_URL:-}"
  check_public_url "storage" "${PUBLIC_STORAGE_HEALTHCHECK_URL:-}"
  if [[ -n "${FRONTEND_HEALTHCHECK_URL:-}" ]]; then
    check_public_url "frontend" "${FRONTEND_HEALTHCHECK_URL}"
  fi
  check_minio_cors_preflight
}

validate_static_configuration

if [[ "${static_config_valid}" != "true" ]]; then
  printf 'SUMMARY pass=%d warn=%d fail=%d\n' "${pass_count}" "${warn_count}" "${fail_count}"
  exit 1
fi

clear_inherited_app_env
if load_app_env_safely; then
  env_loaded=true
  pass envfile "protected env file passed strict non-executing parsing"
else
  fail envfile "env file is missing, insecure, or malformed"
fi

if [[ "${env_loaded}" == "true" ]]; then
  check_environment
fi
check_caddyfile
check_docker
check_disk
check_memory
check_release
check_backup
check_public_endpoints

printf 'SUMMARY pass=%d warn=%d fail=%d\n' "${pass_count}" "${warn_count}" "${fail_count}"
if (( fail_count > 0 )); then
  exit 1
fi
