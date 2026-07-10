#!/usr/bin/env bash
set -euo pipefail
umask 077

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

bin_dir="${tmpdir}/bin"
mkdir -p "${bin_dir}"
log_file="${tmpdir}/docker.log"

cat >"${bin_dir}/docker" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
: "${FAKE_DOCKER_STATE_DIR:?}"
printf 'docker' >> "${FAKE_DOCKER_LOG}"
for arg in "$@"; do
  printf ' %q' "$arg" >> "${FAKE_DOCKER_LOG}"
done
printf '\n' >> "${FAKE_DOCKER_LOG}"

if [[ -n "${FAKE_RUNTIME_ENV_CAPTURE:-}" ]]; then
  previous=''
  for arg in "$@"; do
    if [[ "${previous}" == "--env-file" ]]; then
      cp -- "${arg}" "${FAKE_RUNTIME_ENV_CAPTURE}"
      break
    fi
    previous="${arg}"
  done
fi
if [[ -n "${FAKE_CORS_CAPTURE:-}" ]]; then
  for arg in "$@"; do
    if [[ "${arg}" == *:/tmp/cors.xml:ro ]]; then
      cp -- "${arg%:/tmp/cors.xml:ro}" "${FAKE_CORS_CAPTURE}"
      break
    fi
  done
fi
if [[ -n "${FAKE_MINIO_SCRIPT_CAPTURE:-}" ]]; then
  for arg in "$@"; do
    if [[ "${arg}" == *:/tmp/configure-minio.sh:ro ]]; then
      cp -- "${arg%:/tmp/configure-minio.sh:ro}" "${FAKE_MINIO_SCRIPT_CAPTURE}"
      chmod 0700 "${FAKE_MINIO_SCRIPT_CAPTURE}"
      break
    fi
  done
fi

container_path() {
  printf '%s/%s' "${FAKE_DOCKER_STATE_DIR}" "${1#/}"
}

remove_container() {
  local path
  path="$(container_path "$1")"
  rm -f "${path}" "${path}.running" "${path}.image_ref" "${path}.image_id" "${path}.mounts" "${path}.health"
}

move_container() {
  local old_path new_path suffix
  old_path="$(container_path "$1")"
  new_path="$(container_path "$2")"
  for suffix in '' .running .image_ref .image_id .mounts .health; do
    if [[ -e "${old_path}${suffix}" ]]; then
      mv "${old_path}${suffix}" "${new_path}${suffix}"
    fi
  done
}

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
    if has_arg pg_restore "$@" && has_arg --list "$@"; then
      if [[ "${FAIL_PGRESTORE_VALIDATION:-false}" == "true" ]]; then
        exit 73
      fi
      exit 0
    fi
    if [[ "${3:-}" == "pg_dump" ]]; then
      if [[ "${FAIL_PGDUMP:-false}" == "true" ]]; then
        exit 70
      fi
      printf 'fake-custom-format-pg-dump'
    fi
    exit 0
    ;;
  inspect)
    name="${2:-}"
    if [[ "${2:-}" == "-f" ]]; then
      template="${3:-}"
      name="${4:-}"
    else
      template=''
    fi
    path="$(container_path "${name}")"
    if [[ ! -e "${path}" ]]; then
      exit 1
    fi
    case "${template}" in
      *State.Running*) cat "${path}.running" ;;
      *Config.Healthcheck*) cat "${path}.health" ;;
      *Config.Image*) cat "${path}.image_ref" ;;
      *'{{.Image}}'*) cat "${path}.image_id" ;;
      *'.Mounts'*) cat "${path}.mounts" ;;
      *) printf '{}\n' ;;
    esac
    ;;
  image)
    if [[ "${2:-}" != "inspect" ]]; then
      exit 0
    fi
    if [[ "${3:-}" == "-f" ]]; then
      image_ref="${5:-}"
      case "${image_ref}" in
        postgres@sha256:*) printf 'sha256:%064d\n' 1 ;;
        minio/minio@sha256:*) printf 'sha256:%064d\n' 2 ;;
        minio/mc@sha256:*) printf 'sha256:%064d\n' 3 ;;
        caddy@sha256:*|sha256:0000000000000000000000000000000000000000000000000000000000000004)
          printf 'sha256:%064d\n' 4
          ;;
        *) printf 'sha256:new-backend-image-id\n' ;;
      esac
    fi
    exit 0
    ;;
  run)
    if has_arg /tmp/configure-minio.sh "$@"; then
      if has_arg MINIO_CORS_VERIFY_ONLY=true "$@"; then
        [[ "${FAKE_MINIO_CORS_VERIFY_FAIL:-false}" != "true" ]] || exit 79
        printf 'SCC_MINIO_CORS_RESULT=global-verified\n'
      else
        [[ "${FAKE_MINIO_CORS_HELPER_FAIL:-false}" != "true" ]] || exit 78
        printf 'SCC_MINIO_CORS_RESULT=%s\n' "${FAKE_MINIO_CORS_RESULT:-bucket}"
      fi
      exit 0
    fi
    if has_arg /app/scc-migrate "$@"; then
      migration_command="${@: -1}"
      if [[ "${FAIL_MIGRATION_STEP:-}" == "${migration_command}" ]]; then
        echo "simulated migration ${migration_command} failure" >&2
        exit 74
      fi
      case "${migration_command}" in
        validate)
          printf '{"valid":true,"migrations":['
          printf '{"version":20260703010000,"name":"20260703010000_init_schema.sql","checksum":"%064d"},' 0
          printf '{"version":20260710020000,"name":"20260710020000_phase1_constraints.sql","checksum":"%064d"}' 0
          printf ']}\n'
          ;;
        check)
          printf '{"ok":true,"violations":[]}\n'
          ;;
        status)
          if [[ "${FAKE_MIGRATION_NOOP:-false}" == "true" || -e "${FAKE_DOCKER_STATE_DIR}/.migration-up" ]]; then
            printf '{"ledgerExists":true,"currentVersion":20260710020000,"migrations":['
            printf '{"version":20260703010000,"state":"applied"},'
            printf '{"version":20260710020000,"state":"applied"}'
            printf ']}\n'
          else
            printf '{"ledgerExists":true,"currentVersion":20260703010000,"migrations":['
            printf '{"version":20260703010000,"state":"applied"},'
            printf '{"version":20260710020000,"state":"pending"}'
            printf ']}\n'
          fi
          ;;
        up)
          if [[ "${FAKE_MIGRATION_NOOP:-false}" == "true" ]]; then
            printf '{"ok":true,"applied":null,"appliedCount":0,"currentVersion":20260710020000}\n'
          else
            : >"${FAKE_DOCKER_STATE_DIR}/.migration-up"
            printf '{"ok":true,"applied":[{"version":20260710020000}],"appliedCount":1,"currentVersion":20260710020000}\n'
          fi
          ;;
        version)
          printf '{"ledgerExists":true,"currentVersion":20260710020000}\n'
          ;;
        *)
          echo "unexpected migration command" >&2
          exit 75
          ;;
      esac
      exit 0
    fi
    for arg in "$@"; do
      if [[ "${arg}" == "validate" ]]; then
        [[ "${FAIL_CADDY_VALIDATE:-false}" != "true" ]] || exit 65
        exit 0
      fi
    done
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
      if [[ "${FAIL_RUN_CONTAINER:-}" == "${name}" ]]; then
        exit 71
      fi
      path="$(container_path "${name}")"
      : >"${path}"
      printf 'true\n' >"${path}.running"
      health_command=''
      health_revision=''
      previous=''
      for arg in "$@"; do
        case "${previous}" in
          --health-cmd) health_command="${arg}" ;;
          --label)
            case "${arg}" in
              io.smartcover.healthcheck=*) health_revision="${arg#io.smartcover.healthcheck=}" ;;
            esac
            ;;
        esac
        previous="${arg}"
      done
      if [[ -n "${health_command}" && -n "${health_revision}" ]]; then
        printf 'CMD-SHELL|%s|%s\n' "${health_command}" "${health_revision}" >"${path}.health"
      else
        printf 'none||\n' >"${path}.health"
      fi
      if [[ "${name}" == scc-caddy ]]; then
        printf 'sha256:%064d\n' 4 >"${path}.image_ref"
        printf 'sha256:%064d\n' 4 >"${path}.image_id"
      else
        printf '%s\n' "${FAKE_TARGET_IMAGE_REF:-sha256:new-backend-image-id}" >"${path}.image_ref"
        printf 'sha256:new-backend-image-id\n' >"${path}.image_id"
      fi
    fi
    exit 0
    ;;
  rm)
    remove_container "${@: -1}"
    ;;
  stop)
    printf 'false\n' >"$(container_path "${2}").running"
    ;;
  start|restart)
    if [[ "${FAIL_START_CONTAINER:-}" == "${2}" ]]; then
      exit 73
    fi
    printf 'true\n' >"$(container_path "${2}").running"
    ;;
  rename)
    if [[ "${FAIL_RENAME_SOURCE:-}" == "${2}" ]]; then
      exit 72
    fi
    move_container "${2}" "${3}"
    ;;
  login)
    read -r _token || true
    ;;
  volume|network|pull|logs)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
SH
chmod +x "${bin_dir}/docker"

cat >"${bin_dir}/mc" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
: "${FAKE_MC_LOG:?}"
printf 'mc' >>"${FAKE_MC_LOG}"
for arg in "$@"; do printf ' %q' "${arg}" >>"${FAKE_MC_LOG}"; done
printf '\n' >>"${FAKE_MC_LOG}"
case "${1:-} ${2:-} ${3:-} ${4:-}" in
  'alias set '*|'mb --ignore-existing '*|'anonymous set none '*)
    exit 0
    ;;
  'cors set '*)
    [[ "${FAKE_MC_BUCKET_CORS:-supported}" == 'supported' ]]
    ;;
  'admin config get scc')
    printf 'api cors_allow_origin=%s requests_max=0\n' "$(cat "${FAKE_MC_CORS_STATE}")"
    ;;
  'admin config set scc')
    [[ "${FAKE_MC_GLOBAL_SET_FAIL:-false}" != 'true' ]] || exit 80
    value="${6:-}"
    [[ "${value}" == cors_allow_origin=* ]] || exit 81
    printf '%s\n' "${value#cors_allow_origin=}" >"${FAKE_MC_CORS_STATE}"
    ;;
  *)
    echo 'unsupported fake mc invocation' >&2
    exit 82
    ;;
esac
SH
chmod +x "${bin_dir}/mc"

cat >"${bin_dir}/curl" <<'SH'
#!/usr/bin/env bash
: "${FAKE_CURL_LOG:?}"
printf 'curl' >>"${FAKE_CURL_LOG}"
for arg in "$@"; do
  printf ' %q' "${arg}" >>"${FAKE_CURL_LOG}"
done
printf '\n' >>"${FAKE_CURL_LOG}"
url="${@: -1}"
if [[ "${FAIL_CANDIDATE_HEALTH:-false}" == "true" && "${url}" == *':18080/'* ]]; then
  exit 22
fi
if [[ "${FAIL_REPLACEMENT_BACKEND_HEALTH:-false}" == "true" && "${url}" == *':8080/'* ]]; then
  backend_image_file="${FAKE_DOCKER_STATE_DIR}/scc-backend.image_ref"
  if [[ -f "${backend_image_file}" && "$(cat "${backend_image_file}")" != 'ghcr.io/example/scc-backend:old' ]]; then
    exit 22
  fi
fi
if [[ "${FAIL_PUBLIC_API:-false}" == "true" && "${url}" == https://api.* ]]; then
  exit 22
fi
if [[ "${FAIL_PUBLIC_STORAGE:-false}" == "true" && "${url}" == https://storage.* ]]; then
  exit 22
fi
exit 0
SH
chmod +x "${bin_dir}/curl"

cat >"${bin_dir}/flock" <<'SH'
#!/usr/bin/env bash
[[ "${FAIL_DEPLOY_LOCK:-false}" != "true" ]]
SH
chmod +x "${bin_dir}/flock"

cat >"${bin_dir}/sleep" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "${bin_dir}/sleep"

cat >"${bin_dir}/sudo" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "${bin_dir}/sudo"

docker_state_dir="${tmpdir}/docker-state"
curl_log="${tmpdir}/curl.log"
mkdir -p "${docker_state_dir}"
: >"${curl_log}"

init_container() {
  local name="$1" image_ref="$2" image_id="$3" running="${4:-true}"
  : >"${docker_state_dir}/${name}"
  printf '%s\n' "${running}" >"${docker_state_dir}/${name}.running"
  printf '%s\n' "${image_ref}" >"${docker_state_dir}/${name}.image_ref"
  printf '%s\n' "${image_id}" >"${docker_state_dir}/${name}.image_id"
  case "${name}" in
    scc-backend)
      printf 'CMD-SHELL|%s|%s\n' 'wget -q -T 5 -O /dev/null "http://127.0.0.1:${PORT}/api/v1/readyz"' backend-readyz-v1 >"${docker_state_dir}/${name}.health"
      ;;
    scc-postgres)
      printf 'volume|scc-postgres-data|/var/lib/postgresql/data|true\n' >"${docker_state_dir}/${name}.mounts"
      printf 'CMD-SHELL|%s|%s\n' 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"' postgres-pg-isready-v1 >"${docker_state_dir}/${name}.health"
      ;;
    scc-minio)
      printf 'volume|scc-minio-data|/data|true\n' >"${docker_state_dir}/${name}.mounts"
      printf 'CMD-SHELL|%s|%s\n' 'curl --fail --silent --show-error --max-time 5 http://127.0.0.1:9000/minio/health/ready >/dev/null' minio-ready-v1 >"${docker_state_dir}/${name}.health"
      ;;
    scc-caddy)
      : >"${docker_state_dir}/${name}.mounts"
      printf 'CMD-SHELL|%s|%s\n' 'curl --fail --silent --show-error --max-time 5 http://127.0.0.1:2019/config/ >/dev/null' caddy-admin-v1 >"${docker_state_dir}/${name}.health"
      ;;
    *)
      : >"${docker_state_dir}/${name}.mounts"
      printf 'none||\n' >"${docker_state_dir}/${name}.health"
      ;;
  esac
}

reset_runtime_state() {
  rm -rf "${docker_state_dir}"
  mkdir -p "${docker_state_dir}"
  init_container scc-backend ghcr.io/example/scc-backend:old sha256:old-backend-image-id
  init_container scc-postgres postgres@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb sha256:0000000000000000000000000000000000000000000000000000000000000001
  init_container scc-minio minio/minio@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc sha256:0000000000000000000000000000000000000000000000000000000000000002
  init_container scc-caddy caddy:2-alpine sha256:0000000000000000000000000000000000000000000000000000000000000004
  : >"${log_file}"
  : >"${curl_log}"
}

app_env="${tmpdir}/app.env"
cat >"${app_env}" <<'ENV'
ENV=production
AUTO_MIGRATE=false
SEED_DATA=false
ENABLE_PHASE2_BORROWING=true
RUN_BACKGROUND_JOBS=true
DATABASE_URL=postgresql://smartcover:postgres-password@scc-postgres:5432/smartcover?sslmode=disable
JWT_SECRET=test-secret
POSTGRES_DB=smartcover
POSTGRES_USER=smartcover
POSTGRES_PASSWORD=postgres-password
POSTGRES_IMAGE=postgres@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
MINIO_ROOT_USER=minio-root
MINIO_ROOT_PASSWORD='pa:ss@word/%with;chars'
MINIO_BUCKET='scc;touch /tmp/should-not-exist'
MINIO_INTERNAL_ENDPOINT=scc-minio:9000
MINIO_PUBLIC_ENDPOINT=storage.example.test
MINIO_INTERNAL_USE_SSL=false
MINIO_PUBLIC_USE_SSL=true
MINIO_ACCESS_KEY=minio-root
MINIO_SECRET_KEY='pa:ss@word/%with;chars'
MINIO_CONFIGURE_CORS=best-effort
MINIO_IMAGE=minio/minio@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
MINIO_MC_IMAGE=minio/mc@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
CADDY_IMAGE=caddy@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
API_HOST=api.example.test
STORAGE_HOST=storage.example.test
CORS_ORIGINS=https://app.example.test
ENV

no_public_env="${tmpdir}/no-public.env"
cat >"${no_public_env}" <<'ENV'
ENV=production
AUTO_MIGRATE=false
SEED_DATA=false
ENABLE_PHASE2_BORROWING=true
RUN_BACKGROUND_JOBS=true
DATABASE_URL=postgresql://smartcover:postgres-password@scc-postgres:5432/smartcover?sslmode=disable
JWT_SECRET=test-secret
POSTGRES_DB=smartcover
POSTGRES_USER=smartcover
POSTGRES_PASSWORD=postgres-password
POSTGRES_IMAGE=postgres@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
MINIO_ROOT_USER=minio-root
MINIO_ROOT_PASSWORD='pa:ss@word/%with;chars'
MINIO_BUCKET=scc
MINIO_INTERNAL_ENDPOINT=scc-minio:9000
MINIO_PUBLIC_ENDPOINT=storage.example.test
MINIO_INTERNAL_USE_SSL=false
MINIO_PUBLIC_USE_SSL=true
MINIO_ACCESS_KEY=minio-root
MINIO_SECRET_KEY='pa:ss@word/%with;chars'
MINIO_CONFIGURE_CORS=best-effort
MINIO_IMAGE=minio/minio@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
MINIO_MC_IMAGE=minio/mc@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
CADDY_IMAGE=caddy@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee
CORS_ORIGINS=https://app.example.test
ENV

caddyfile_path="${tmpdir}/host-state/Caddyfile"
deploy_state_dir="${tmpdir}/deploy-state"
immutable_image="ghcr.io/example/scc-backend@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

assert_native_healthcheck() {
  local label="$1" run_line="$2" revision="$3" command_marker="$4"
  local interval="$5" timeout="$6" retries="$7" start_period="$8"
  if [[ -z "${run_line}" || "${run_line}" != *"--label io.smartcover.healthcheck=${revision}"* || \
    "${run_line}" != *'--health-cmd '* || "${run_line}" != *"${command_marker}"* || \
    "${run_line}" != *"--health-interval ${interval}"* || "${run_line}" != *"--health-timeout ${timeout}"* || \
    "${run_line}" != *"--health-retries ${retries}"* || "${run_line}" != *"--health-start-period ${start_period}"* ]]; then
    echo "FAIL: ${label} Docker run did not contain the managed native healthcheck contract" >&2
    exit 1
  fi
}

wildcard_cors_env="${tmpdir}/wildcard-cors.env"
cp "${no_public_env}" "${wildcard_cors_env}"
printf 'CORS_ORIGINS=*\n' >>"${wildcard_cors_env}"
if PATH="${bin_dir}:${PATH}" \
  APP_ENV_PATH="${wildcard_cors_env}" \
  GHCR_IMAGE="${immutable_image}" \
  DEPLOY_STATE_DIR="${tmpdir}/wildcard-cors-deploy" \
  RELEASE_ID="wildcard-cors-release" \
  MANAGE_CADDY=false \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: production deploy accepted wildcard MinIO CORS" >&2
  exit 1
fi

assert_production_guard() {
  local label="$1" env_path="$2"
  shift 2
  : >"${log_file}"
  if env PATH="${bin_dir}:${PATH}" \
    FAKE_DOCKER_LOG="${log_file}" \
    FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
    FAKE_CURL_LOG="${curl_log}" \
    APP_ENV_PATH="${env_path}" \
    GHCR_IMAGE="${immutable_image}" \
    DEPLOY_STATE_DIR="${tmpdir}/${label}-deploy" \
    RELEASE_ID="${label}-release" \
    MANAGE_CADDY=false \
    "$@" \
    "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
    echo "FAIL: production deploy accepted ${label}" >&2
    exit 1
  fi
  if [[ -s "${log_file}" ]]; then
    echo "FAIL: ${label} was rejected only after Docker work" >&2
    exit 1
  fi
}

non_production_env="${tmpdir}/non-production.env"
sed 's/^ENV=production$/ENV=development/' "${app_env}" >"${non_production_env}"
assert_production_guard non-production "${non_production_env}"

auto_migrate_env="${tmpdir}/auto-migrate.env"
sed 's/^AUTO_MIGRATE=false$/AUTO_MIGRATE=true/' "${app_env}" >"${auto_migrate_env}"
assert_production_guard auto-migrate "${auto_migrate_env}"

seed_data_env="${tmpdir}/seed-data.env"
sed 's/^SEED_DATA=false$/SEED_DATA=true/' "${app_env}" >"${seed_data_env}"
assert_production_guard seed-data "${seed_data_env}"

invalid_phase2_env="${tmpdir}/invalid-phase2.env"
sed 's/^ENABLE_PHASE2_BORROWING=true$/ENABLE_PHASE2_BORROWING=TRUE/' "${app_env}" >"${invalid_phase2_env}"
assert_production_guard invalid-phase2 "${invalid_phase2_env}"

disabled_jobs_env="${tmpdir}/disabled-jobs.env"
sed 's/^RUN_BACKGROUND_JOBS=true$/RUN_BACKGROUND_JOBS=false/' "${app_env}" >"${disabled_jobs_env}"
assert_production_guard disabled-jobs "${disabled_jobs_env}"

mutable_infra_env="${tmpdir}/mutable-infra.env"
sed 's#^POSTGRES_IMAGE=.*#POSTGRES_IMAGE=postgres:16-alpine#' "${app_env}" >"${mutable_infra_env}"
assert_production_guard mutable-infra-image "${mutable_infra_env}"

database_route_drift_env="${tmpdir}/database-route-drift.env"
sed 's/@scc-postgres:5432/@unmanaged-postgres:5432/' "${app_env}" >"${database_route_drift_env}"
assert_production_guard database-route-drift "${database_route_drift_env}"

database_query_override_env="${tmpdir}/database-query-override.env"
sed 's/?sslmode=disable/?sslmode=disable\&host=unmanaged-postgres/' "${app_env}" >"${database_query_override_env}"
assert_production_guard database-query-override "${database_query_override_env}"

assert_production_guard optional-backup "${app_env}" PREDEPLOY_BACKUP_REQUIRED=false
assert_production_guard malformed-source-commit "${app_env}" $'SOURCE_COMMIT=bad\ncommit'
assert_production_guard invalid-health-timeout "${app_env}" HEALTHCHECK_TIMEOUT_SECONDS=0
assert_production_guard mismatched-app-port "${app_env}" APP_PORT=8081

malicious_env="${tmpdir}/malicious.env"
cp "${app_env}" "${malicious_env}"
printf 'UNUSED_COMMAND=$(touch %s)\n' "${tmpdir}/env-command-ran" >>"${malicious_env}"
assert_production_guard executable-env-syntax "${malicious_env}"
if [[ -e "${tmpdir}/env-command-ran" ]]; then
  echo "FAIL: deploy executed command syntax from APP_ENV_PATH" >&2
  exit 1
fi

insecure_env="${tmpdir}/insecure.env"
cp "${app_env}" "${insecure_env}"
chmod 0644 "${insecure_env}"
assert_production_guard insecure-env-permissions "${insecure_env}"

for drifted_service in postgres minio; do
  reset_runtime_state
  case "${drifted_service}" in
    postgres)
      printf 'volume|wrong-postgres-volume|/var/lib/postgresql/data|true\n' >"${docker_state_dir}/scc-postgres.mounts"
      expected_volume=scc-postgres-data
      ;;
    minio)
      printf 'volume|wrong-minio-volume|/data|true\n' >"${docker_state_dir}/scc-minio.mounts"
      expected_volume=scc-minio-data
      ;;
  esac
  drift_stderr="${tmpdir}/${drifted_service}-volume-drift.stderr"
  if PATH="${bin_dir}:${PATH}" \
    FAKE_DOCKER_LOG="${log_file}" \
    FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
    FAKE_CURL_LOG="${curl_log}" \
    FAKE_TARGET_IMAGE_REF="${immutable_image}" \
    APP_ENV_PATH="${app_env}" \
    GHCR_IMAGE="${immutable_image}" \
    DEPLOY_STATE_DIR="${tmpdir}/${drifted_service}-volume-drift-deploy" \
    RELEASE_ID="${drifted_service}-volume-drift-release" \
    MANAGE_CADDY=false \
    "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>"${drift_stderr}"; then
    echo "FAIL: deploy accepted ${drifted_service} persistent-volume drift" >&2
    exit 1
  fi
  if ! grep -Fq "must mount writable named volume ${expected_volume}" "${drift_stderr}"; then
    echo "FAIL: ${drifted_service} volume drift did not identify the required persistent volume" >&2
    exit 1
  fi
  if grep -Eq 'pg_dump|--entrypoint /app/scc-migrate|--name scc-backend-candidate' "${log_file}"; then
    echo "FAIL: deploy reached backup, migration, or candidate work after ${drifted_service} volume drift" >&2
    exit 1
  fi
done

reset_runtime_state
mkdir -p "$(dirname "${caddyfile_path}")"
printf 'old-known-good-caddy-config\n' >"${caddyfile_path}"
runtime_env_capture="${tmpdir}/runtime.env.capture"
cors_capture="${tmpdir}/cors.xml.capture"
minio_script_capture="${tmpdir}/configure-minio.sh.capture"

PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_LOG="${log_file}" \
FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
FAKE_CURL_LOG="${curl_log}" \
FAKE_TARGET_IMAGE_REF="${immutable_image}" \
FAKE_RUNTIME_ENV_CAPTURE="${runtime_env_capture}" \
FAKE_CORS_CAPTURE="${cors_capture}" \
FAKE_MINIO_SCRIPT_CAPTURE="${minio_script_capture}" \
APP_ENV_PATH="${app_env}" \
GHCR_IMAGE="${immutable_image}" \
CADDYFILE_PATH="${caddyfile_path}" \
DEPLOY_STATE_DIR="${deploy_state_dir}" \
RELEASE_ID="success-release" \
SOURCE_COMMIT="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
FRONTEND_HEALTHCHECK_URL="https://app.example.test/login" \
MANAGE_CADDY=true \
"${repo_root}/scripts/deploy-vps.sh" >/dev/null

if ! grep -Fqx 'MINIO_SECRET_KEY=pa:ss@word/%with;chars' "${runtime_env_capture}" || \
  ! grep -Fqx 'MINIO_BUCKET=scc;touch /tmp/should-not-exist' "${runtime_env_capture}" || \
  grep -Fq "'pa:ss@word/%with;chars'" "${runtime_env_capture}"; then
  echo "FAIL: protected env values were not normalized to Docker env-file semantics" >&2
  exit 1
fi
if grep -Eq '^(AUTO_MIGRATE|SEED_DATA|RUN_BACKGROUND_JOBS|POSTGRES_PASSWORD|MINIO_ROOT_USER|MINIO_ROOT_PASSWORD|POSTGRES_IMAGE|MINIO_IMAGE|CADDY_IMAGE)=' "${runtime_env_capture}"; then
  echo "FAIL: application runtime env retained explicit overrides, infrastructure credentials, or image controls" >&2
  exit 1
fi
if find "${deploy_state_dir}" -maxdepth 1 \( -name '.runtime-env.*' -o -name '.migration-env.*' \) -print -quit | grep -q .; then
  echo "FAIL: normalized runtime env file was not removed after deploy" >&2
  exit 1
fi
if ! grep -Fq '<AllowedOrigin>https://app.example.test</AllowedOrigin>' "${cors_capture}" || \
  grep -Fq '<AllowedOrigin>*</AllowedOrigin>' "${cors_capture}"; then
  echo "FAIL: MinIO CORS generation did not receive the validated production origins" >&2
  exit 1
fi

fake_mc_log="${tmpdir}/mc.log"
fake_mc_cors_state="${tmpdir}/mc-cors.state"
: >"${fake_mc_log}"
printf '*\n' >"${fake_mc_cors_state}"
helper_output="$(printf 'root-user\nroot-password\n' | env \
  PATH="${bin_dir}:${PATH}" \
  FAKE_MC_LOG="${fake_mc_log}" \
  FAKE_MC_CORS_STATE="${fake_mc_cors_state}" \
  FAKE_MC_BUCKET_CORS=unsupported \
  MINIO_CONFIGURE_CORS=best-effort \
  MINIO_CORS_ORIGINS=https://app.example.test \
  "${minio_script_capture}" scc-minio scc "${cors_capture}" 2>/dev/null)"
if [[ "${helper_output}" != *'SCC_MINIO_CORS_RESULT=global-changed'* ]] || \
  [[ "$(cat "${fake_mc_cors_state}")" != 'https://app.example.test' ]] || \
  ! grep -Fq 'mc admin config set scc api cors_allow_origin=https://app.example.test' "${fake_mc_log}"; then
  echo "FAIL: unsupported bucket CORS did not use and verify exact global API fallback" >&2
  exit 1
fi

: >"${fake_mc_log}"
helper_output="$(printf 'root-user\nroot-password\n' | env \
  PATH="${bin_dir}:${PATH}" \
  FAKE_MC_LOG="${fake_mc_log}" \
  FAKE_MC_CORS_STATE="${fake_mc_cors_state}" \
  FAKE_MC_BUCKET_CORS=unsupported \
  MINIO_CONFIGURE_CORS=required \
  MINIO_CORS_ORIGINS=https://app.example.test \
  "${minio_script_capture}" scc-minio scc "${cors_capture}" 2>/dev/null)"
if [[ "${helper_output}" != *'SCC_MINIO_CORS_RESULT=global-unchanged'* ]] || grep -q 'admin config set' "${fake_mc_log}"; then
  echo "FAIL: exact existing global API CORS should not be rewritten or restarted" >&2
  exit 1
fi

if printf 'root-user\nroot-password\n' | env \
  PATH="${bin_dir}:${PATH}" \
  FAKE_MC_LOG="${fake_mc_log}" \
  FAKE_MC_CORS_STATE="${fake_mc_cors_state}" \
  MINIO_CORS_VERIFY_ONLY=true \
  MINIO_CONFIGURE_CORS=required \
  MINIO_CORS_ORIGINS=https://unexpected.example.test \
  "${minio_script_capture}" scc-minio scc "${cors_capture}" >/dev/null 2>&1; then
  echo "FAIL: post-restart global API CORS verification accepted drift" >&2
  exit 1
fi

printf '*\n' >"${fake_mc_cors_state}"
helper_output="$(printf 'root-user\nroot-password\n' | env \
  PATH="${bin_dir}:${PATH}" \
  FAKE_MC_LOG="${fake_mc_log}" \
  FAKE_MC_CORS_STATE="${fake_mc_cors_state}" \
  FAKE_MC_BUCKET_CORS=unsupported \
  FAKE_MC_GLOBAL_SET_FAIL=true \
  MINIO_CONFIGURE_CORS=best-effort \
  MINIO_CORS_ORIGINS=https://app.example.test \
  "${minio_script_capture}" scc-minio scc "${cors_capture}" 2>/dev/null)"
if [[ "${helper_output}" != *'SCC_MINIO_CORS_RESULT=unconfigured'* ]]; then
  echo "FAIL: best-effort mode did not report failure of both CORS mechanisms" >&2
  exit 1
fi
if printf 'root-user\nroot-password\n' | env \
  PATH="${bin_dir}:${PATH}" \
  FAKE_MC_LOG="${fake_mc_log}" \
  FAKE_MC_CORS_STATE="${fake_mc_cors_state}" \
  FAKE_MC_BUCKET_CORS=unsupported \
  FAKE_MC_GLOBAL_SET_FAIL=true \
  MINIO_CONFIGURE_CORS=required \
  MINIO_CORS_ORIGINS=https://app.example.test \
  "${minio_script_capture}" scc-minio scc "${cors_capture}" >/dev/null 2>&1; then
  echo "FAIL: required mode continued after both CORS mechanisms failed" >&2
  exit 1
fi

if grep -q 'MC_HOST_scc=http' "${log_file}"; then
  echo "FAIL: MinIO credentials were embedded in MC_HOST_scc URL" >&2
  exit 1
fi

if grep -E -q 'test-secret|postgres-password|pa:ss@word' "${log_file}"; then
  echo "FAIL: deploy exposed a secret in Docker command arguments" >&2
  exit 1
fi

if grep -q ' sh -c ' "${log_file}"; then
  echo "FAIL: MinIO bucket commands still use string-built sh -c" >&2
  exit 1
fi

if [[ ! -f "${caddyfile_path}" ]]; then
  echo "FAIL: Caddyfile must remain on the host after deploy exits" >&2
  exit 1
fi

if ! grep -q '^api\.example\.test {' "${caddyfile_path}"; then
  echo "FAIL: persistent Caddyfile is missing the API host" >&2
  exit 1
fi

caddy_run_line="$(grep 'docker run -d --name scc-caddy ' "${log_file}" | tail -n 1 || true)"
if [[ -z "${caddy_run_line}" ]]; then
  echo "FAIL: deploy did not recreate the managed Caddy container" >&2
  exit 1
fi

if [[ "${caddy_run_line}" != *"${caddyfile_path}:/etc/caddy/Caddyfile:ro"* ]]; then
  echo "FAIL: Caddy container must read-only bind-mount the persistent Caddyfile" >&2
  exit 1
fi
assert_native_healthcheck caddy "${caddy_run_line}" caddy-admin-v1 '127.0.0.1:2019/config/' 30s 10s 3 20s

if find "$(dirname "${caddyfile_path}")" -name '.Caddyfile.tmp.*' -print -quit | grep -q .; then
  echo "FAIL: staged Caddyfile was not cleaned up after atomic install" >&2
  exit 1
fi

success_release_dir="${deploy_state_dir}/releases/success-release"
if [[ ! -s "${deploy_state_dir}/predeploy-backups/success-release.dump" ]]; then
  echo "FAIL: deploy did not create a non-empty predeploy PostgreSQL dump" >&2
  exit 1
fi

if ! grep -q '^result=success$' "${success_release_dir}/result.env"; then
  echo "FAIL: successful deploy release metadata is incomplete" >&2
  exit 1
fi

if ! grep -Fq "target_image_ref=${immutable_image}" "${success_release_dir}/release.env"; then
  echo "FAIL: release metadata did not record the immutable target image" >&2
  exit 1
fi

for image_key in postgres_image_ref postgres_image_id minio_image_ref minio_image_id minio_mc_image_ref minio_mc_image_id caddy_image_ref caddy_image_id; do
  if ! grep -q "^${image_key}=" "${success_release_dir}/release.env"; then
    echo "FAIL: release metadata did not record ${image_key}" >&2
    exit 1
  fi
done

for migration_step in validate check status_before up status_after version; do
  if [[ ! -f "${success_release_dir}/migration_${migration_step}.json" || \
    ! -f "${success_release_dir}/migration_${migration_step}.stderr" ]] || \
    ! grep -q "^migration_${migration_step}_result=passed$" "${success_release_dir}/release.env" || \
    ! grep -Fq "migration_${migration_step}_stdout=${success_release_dir}/migration_${migration_step}.json" "${success_release_dir}/release.env"; then
    echo "FAIL: successful release did not retain ${migration_step} migration evidence" >&2
    exit 1
  fi
done

if ! grep -q '^migration_target_version=20260710020000$' "${success_release_dir}/release.env" || \
  ! grep -q '^migration_version_before=20260703010000$' "${success_release_dir}/release.env" || \
  ! grep -q '^migration_up_version=20260710020000$' "${success_release_dir}/release.env" || \
  ! grep -q '^migration_applied_count=1$' "${success_release_dir}/release.env" || \
  ! grep -q '^migration_version_after=20260710020000$' "${success_release_dir}/release.env" || \
  ! grep -q '^migration_version_verified=20260710020000$' "${success_release_dir}/release.env" || \
  ! grep -q '^migration_contract=passed$' "${success_release_dir}/release.env"; then
  echo "FAIL: successful release metadata did not record the migration version transition" >&2
  exit 1
fi

if grep -R -E -q 'test-secret|postgres-password|pa:ss@word' "${success_release_dir}"; then
  echo "FAIL: release metadata or Caddy snapshot contains a secret" >&2
  exit 1
fi

if [[ "$(cat "${docker_state_dir}/scc-backend.image_ref")" != "${immutable_image}" || "$(cat "${docker_state_dir}/scc-backend.running")" != "true" ]]; then
  echo "FAIL: successful deploy did not leave the immutable backend running" >&2
  exit 1
fi

if [[ -e "${docker_state_dir}/scc-backend-previous" || -e "${docker_state_dir}/scc-caddy-previous" ]]; then
  echo "FAIL: successful deploy did not clean previous containers" >&2
  exit 1
fi

backup_line="$(grep -n 'docker exec scc-postgres pg_dump ' "${log_file}" | head -n 1 | cut -d: -f1)"
backup_validation_line="$(grep -n 'docker exec -i scc-postgres pg_restore --list' "${log_file}" | head -n 1 | cut -d: -f1)"
validate_line="$(grep -n -- '--entrypoint /app/scc-migrate .* validate$' "${log_file}" | head -n 1 | cut -d: -f1)"
check_line="$(grep -n -- '--entrypoint /app/scc-migrate .* check$' "${log_file}" | head -n 1 | cut -d: -f1)"
status_before_line="$(grep -n -- '--entrypoint /app/scc-migrate .* status$' "${log_file}" | head -n 1 | cut -d: -f1)"
up_line="$(grep -n -- '--entrypoint /app/scc-migrate .* up$' "${log_file}" | head -n 1 | cut -d: -f1)"
status_after_line="$(grep -n -- '--entrypoint /app/scc-migrate .* status$' "${log_file}" | tail -n 1 | cut -d: -f1)"
version_line="$(grep -n -- '--entrypoint /app/scc-migrate .* version$' "${log_file}" | head -n 1 | cut -d: -f1)"
candidate_line="$(grep -n 'docker run -d --name scc-backend-candidate ' "${log_file}" | head -n 1 | cut -d: -f1)"
switch_line="$(grep -n 'docker stop scc-backend' "${log_file}" | head -n 1 | cut -d: -f1)"
if [[ -z "${backup_line}" || -z "${backup_validation_line}" || -z "${validate_line}" || -z "${check_line}" || -z "${status_before_line}" || \
  -z "${up_line}" || -z "${status_after_line}" || -z "${version_line}" || -z "${candidate_line}" || -z "${switch_line}" ]] || \
  (( backup_line >= backup_validation_line || backup_validation_line >= validate_line || validate_line >= check_line || check_line >= status_before_line || \
    status_before_line >= up_line || up_line >= status_after_line || status_after_line >= version_line || \
    version_line >= candidate_line || candidate_line >= switch_line )); then
  echo "FAIL: expected backup/verification -> validate/check/status/up/status/version -> candidate -> switch ordering" >&2
  exit 1
fi

migration_run_lines="$(grep -- '--entrypoint /app/scc-migrate ' "${log_file}" || true)"
if [[ "$(printf '%s\n' "${migration_run_lines}" | grep -c .)" -ne 6 ]] || \
  printf '%s\n' "${migration_run_lines}" | grep -v -- '--env-file .*\.migration-env\..* -e AUTO_MIGRATE=false -e SEED_DATA=false -e RUN_BACKGROUND_JOBS=false ' >/dev/null; then
  echo "FAIL: migration one-shot containers did not use the locked-down runtime environment" >&2
  exit 1
fi

candidate_run_line="$(grep 'docker run -d --name scc-backend-candidate ' "${log_file}" | head -n 1 || true)"
active_run_line="$(grep 'docker run -d --name scc-backend --restart ' "${log_file}" | head -n 1 || true)"
if [[ "${candidate_run_line}" != *'-e AUTO_MIGRATE=false -e SEED_DATA=false -e RUN_BACKGROUND_JOBS=false '* ]]; then
  echo "FAIL: candidate did not explicitly disable migrations, seeds, and background jobs" >&2
  exit 1
fi
if [[ "${active_run_line}" != *'-e AUTO_MIGRATE=false -e SEED_DATA=false -e RUN_BACKGROUND_JOBS=true '* ]]; then
  echo "FAIL: active backend did not explicitly enable its single background-job owner" >&2
  exit 1
fi
assert_native_healthcheck 'backend candidate' "${candidate_run_line}" backend-readyz-v1 'api/v1/readyz' 30s 10s 3 30s
assert_native_healthcheck 'active backend' "${active_run_line}" backend-readyz-v1 'api/v1/readyz' 30s 10s 3 30s
if [[ "${candidate_run_line}" != *'-e PORT=8080 '* || "${active_run_line}" != *'-e PORT=8080 '* ]]; then
  echo 'FAIL: backend healthcheck port was not made explicit in both runtime containers' >&2
  exit 1
fi

if ! grep -q 'docker restart scc-caddy' "${log_file}"; then
  echo "FAIL: deploy did not prove Caddy restart persistence" >&2
  exit 1
fi

if ! grep -Fq 'https://app.example.test/login' "${curl_log}"; then
  echo "FAIL: configured frontend health gate was not checked" >&2
  exit 1
fi
if grep -v -- '--max-time 10 ' "${curl_log}" | grep -q .; then
  echo "FAIL: one or more release health checks lacked a bounded curl timeout" >&2
  exit 1
fi

reset_runtime_state
if ! PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAKE_MINIO_CORS_RESULT=global-changed \
  APP_ENV_PATH="${no_public_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="global-cors-fallback-release" \
  MANAGE_CADDY=false \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null; then
  echo "FAIL: deploy rejected the supported global MinIO CORS fallback" >&2
  exit 1
fi
if [[ "$(grep -c '^docker restart scc-minio$' "${log_file}")" -ne 1 ]] || \
  ! grep -q '^minio_cors_result=global-verified$' "${deploy_state_dir}/releases/global-cors-fallback-release/release.env" || \
  ! grep -q '^minio_cors_restart=performed$' "${deploy_state_dir}/releases/global-cors-fallback-release/release.env"; then
  echo "FAIL: global MinIO CORS fallback did not perform exactly one restart and retain verification evidence" >&2
  exit 1
fi

reset_runtime_state
rm -f \
  "${docker_state_dir}/scc-postgres" \
  "${docker_state_dir}/scc-postgres.running" \
  "${docker_state_dir}/scc-postgres.image_ref" \
  "${docker_state_dir}/scc-postgres.image_id" \
  "${docker_state_dir}/scc-minio" \
  "${docker_state_dir}/scc-minio.running" \
  "${docker_state_dir}/scc-minio.image_ref" \
  "${docker_state_dir}/scc-minio.image_id"
if ! PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="fresh-infrastructure-release" \
  MANAGE_CADDY=false \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null; then
  echo "FAIL: fresh infrastructure deploy did not complete" >&2
  exit 1
fi
if ! grep -q 'docker run -d --name scc-postgres .* --env-file ' "${log_file}" || \
  ! grep -q 'docker run -d --name scc-minio .* --env-file ' "${log_file}" || \
  grep -E -q -- '-e (POSTGRES_PASSWORD|MINIO_ROOT_PASSWORD)=' "${log_file}" || \
  grep -E -q 'postgres-password|pa:ss@word' "${log_file}"; then
  echo "FAIL: fresh infrastructure creation exposed database or object-store credentials in Docker arguments" >&2
  exit 1
fi
postgres_run_line="$(grep 'docker run -d --name scc-postgres ' "${log_file}" | head -n 1 || true)"
minio_run_line="$(grep 'docker run -d --name scc-minio ' "${log_file}" | head -n 1 || true)"
assert_native_healthcheck postgres "${postgres_run_line}" postgres-pg-isready-v1 pg_isready 10s 5s 5 60s
assert_native_healthcheck minio "${minio_run_line}" minio-ready-v1 '/minio/health/ready' 30s 10s 3 30s
if find "${deploy_state_dir}" -maxdepth 1 \( -name '.runtime-env.*' -o -name '.migration-env.*' -o -name '.postgres-env.*' -o -name '.minio-env.*' \) -print -quit | grep -q .; then
  echo "FAIL: short-lived Docker env files were not cleaned after infrastructure creation" >&2
  exit 1
fi

# Existing data containers are reconciled only after the required PostgreSQL
# checkpoint, retain their validated named volumes, and leave no stale previous
# container once the new native healthchecks pass.
reset_runtime_state
printf 'none||\n' >"${docker_state_dir}/scc-postgres.health"
printf 'CMD-SHELL|true|drifted-v0\n' >"${docker_state_dir}/scc-minio.health"
if ! PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  APP_ENV_PATH="${no_public_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="healthcheck-reconciliation-release" \
  MANAGE_CADDY=false \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null; then
  echo "FAIL: deploy could not reconcile missing infrastructure healthchecks" >&2
  exit 1
fi
if ! grep -q '^postgres_healthcheck=reconciled$' "${deploy_state_dir}/releases/healthcheck-reconciliation-release/release.env" || \
  ! grep -q '^minio_healthcheck=reconciled$' "${deploy_state_dir}/releases/healthcheck-reconciliation-release/release.env"; then
  echo "FAIL: release metadata did not record infrastructure healthcheck reconciliation" >&2
  exit 1
fi
if [[ -e "${docker_state_dir}/scc-postgres-healthcheck-previous" || -e "${docker_state_dir}/scc-minio-healthcheck-previous" ]]; then
  echo "FAIL: successful infrastructure healthcheck reconciliation retained a previous container" >&2
  exit 1
fi
backup_before_health_line="$(grep -n 'docker exec scc-postgres pg_dump ' "${log_file}" | head -n 1 | cut -d: -f1)"
postgres_health_stop_line="$(grep -n '^docker stop scc-postgres$' "${log_file}" | head -n 1 | cut -d: -f1)"
minio_health_stop_line="$(grep -n '^docker stop scc-minio$' "${log_file}" | head -n 1 | cut -d: -f1)"
if [[ -z "${backup_before_health_line}" || -z "${postgres_health_stop_line}" || -z "${minio_health_stop_line}" ]] || \
  (( backup_before_health_line >= postgres_health_stop_line || backup_before_health_line >= minio_health_stop_line )); then
  echo "FAIL: infrastructure healthcheck reconciliation did not wait for the required predeploy backup" >&2
  exit 1
fi
postgres_run_line="$(grep 'docker run -d --name scc-postgres ' "${log_file}" | head -n 1 || true)"
minio_run_line="$(grep 'docker run -d --name scc-minio ' "${log_file}" | head -n 1 || true)"
assert_native_healthcheck postgres "${postgres_run_line}" postgres-pg-isready-v1 pg_isready 10s 5s 5 60s
assert_native_healthcheck minio "${minio_run_line}" minio-ready-v1 '/minio/health/ready' 30s 10s 3 30s

# A failed replacement is removed and the original data container is restored
# before the release exits.
reset_runtime_state
printf 'none||\n' >"${docker_state_dir}/scc-postgres.health"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAIL_RUN_CONTAINER=scc-postgres \
  APP_ENV_PATH="${no_public_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="healthcheck-reconciliation-failure" \
  MANAGE_CADDY=false \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: deploy accepted failed PostgreSQL healthcheck reconciliation" >&2
  exit 1
fi
if [[ ! -e "${docker_state_dir}/scc-postgres" || "$(cat "${docker_state_dir}/scc-postgres.running")" != true || \
  -e "${docker_state_dir}/scc-postgres-healthcheck-previous" ]]; then
  echo "FAIL: failed PostgreSQL healthcheck reconciliation did not restore the original container" >&2
  exit 1
fi
if ! grep -q '^postgres_healthcheck=failed-original-restored$' \
  "${deploy_state_dir}/releases/healthcheck-reconciliation-failure/release.env"; then
  echo "FAIL: failed PostgreSQL healthcheck reconciliation did not record its restored outcome" >&2
  exit 1
fi

reset_runtime_state
init_container scc-postgres-healthcheck-previous "${POSTGRES_IMAGE:-postgres@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb}" sha256:old-healthcheck-container
reserved_health_stderr="${tmpdir}/reserved-healthcheck-previous.stderr"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  APP_ENV_PATH="${no_public_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="reserved-healthcheck-previous" \
  MANAGE_CADDY=false \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>"${reserved_health_stderr}"; then
  echo "FAIL: deploy accepted a reserved infrastructure previous container" >&2
  exit 1
fi
if ! grep -q 'reserved deploy container exists' "${reserved_health_stderr}" || \
  grep -Eq 'pg_dump|--entrypoint /app/scc-migrate|--name scc-backend-candidate' "${log_file}"; then
  echo "FAIL: reserved infrastructure previous container was not rejected before release work" >&2
  exit 1
fi

reset_runtime_state
if ! PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  APP_ENV_PATH="${no_public_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="no-public-health-release" \
  MANAGE_CADDY=false \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null; then
  echo "FAIL: deploy without configured public routes should still pass internal health" >&2
  exit 1
fi
if ! grep -q '^public_health=not-configured$' \
  "${deploy_state_dir}/releases/no-public-health-release/release.env"; then
  echo "FAIL: deploy claimed public health without checking a public URL" >&2
  exit 1
fi
if grep -q 'https://' "${curl_log}"; then
  echo "FAIL: deploy without public URLs performed an unexpected public check" >&2
  exit 1
fi

reset_runtime_state
if ! PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAKE_MIGRATION_NOOP=true \
  APP_ENV_PATH="${no_public_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="no-op-migration-release" \
  MANAGE_CADDY=false \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null; then
  echo "FAIL: deploy rejected a valid no-op migration result" >&2
  exit 1
fi
if ! grep -q '^migration_applied_count=0$' \
  "${deploy_state_dir}/releases/no-op-migration-release/release.env" || \
  ! grep -q '^migration_contract=passed$' \
  "${deploy_state_dir}/releases/no-op-migration-release/release.env"; then
  echo "FAIL: no-op migration did not retain verified release evidence" >&2
  exit 1
fi

known_good_caddyfile="${tmpdir}/known-good-Caddyfile"
reset_runtime_state
printf 'old-known-good-caddy-config\n' >"${caddyfile_path}"
cp "${caddyfile_path}" "${known_good_caddyfile}"

if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAIL_CADDY_VALIDATE=true \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="failed-caddy-release" \
  SOURCE_COMMIT="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: deploy must fail when the staged Caddyfile fails validation" >&2
  exit 1
fi

if ! cmp -s "${known_good_caddyfile}" "${caddyfile_path}"; then
  echo "FAIL: failed validation replaced the last known-good Caddyfile" >&2
  exit 1
fi

if grep -q 'docker rm -f scc-caddy' "${log_file}"; then
  echo "FAIL: existing Caddy was removed before the new config passed validation" >&2
  exit 1
fi

if [[ "$(cat "${docker_state_dir}/scc-backend.image_ref")" != 'ghcr.io/example/scc-backend:old' || "$(cat "${docker_state_dir}/scc-backend.running")" != 'true' ]]; then
  echo "FAIL: Caddy validation failure did not restore the previous backend" >&2
  exit 1
fi

if [[ -e "${docker_state_dir}/scc-backend-previous" ]]; then
  echo "FAIL: backend rollback left the previous container stranded" >&2
  exit 1
fi

if ! grep -q '^result=failed$' "${deploy_state_dir}/releases/failed-caddy-release/result.env"; then
  echo "FAIL: failed Caddy release was not recorded" >&2
  exit 1
fi

if find "$(dirname "${caddyfile_path}")" -name '.Caddyfile.tmp.*' -print -quit | grep -q .; then
  echo "FAIL: rejected Caddyfile staging file was not cleaned up" >&2
  exit 1
fi

reset_runtime_state
printf 'backend-run-failure-known-good-caddy\n' >"${caddyfile_path}"
cp "${caddyfile_path}" "${known_good_caddyfile}"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAIL_RUN_CONTAINER=scc-backend \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="failed-backend-run-release" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: replacement backend start failure must fail the deploy" >&2
  exit 1
fi
if [[ "$(cat "${docker_state_dir}/scc-backend.image_ref")" != 'ghcr.io/example/scc-backend:old' || \
  "$(cat "${docker_state_dir}/scc-backend.running")" != 'true' ]]; then
  echo "FAIL: replacement backend start failure did not restore the prior backend" >&2
  exit 1
fi
if [[ "$(cat "${docker_state_dir}/scc-caddy.running")" != 'true' ]] || \
  ! cmp -s "${known_good_caddyfile}" "${caddyfile_path}"; then
  echo "FAIL: backend start failure changed prior Caddy state" >&2
  exit 1
fi
if ! grep -q '^backend_rollback_status=restored$' \
  "${deploy_state_dir}/releases/failed-backend-run-release/release.env"; then
  echo "FAIL: backend start rollback was not recorded" >&2
  exit 1
fi

reset_runtime_state
printf 'backend-health-failure-known-good-caddy\n' >"${caddyfile_path}"
cp "${caddyfile_path}" "${known_good_caddyfile}"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAIL_REPLACEMENT_BACKEND_HEALTH=true \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="failed-backend-health-release" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: unhealthy replacement backend must fail the deploy" >&2
  exit 1
fi
if [[ "$(cat "${docker_state_dir}/scc-backend.image_ref")" != 'ghcr.io/example/scc-backend:old' || \
  "$(cat "${docker_state_dir}/scc-backend.running")" != 'true' ]]; then
  echo "FAIL: replacement backend health failure did not restore the prior backend" >&2
  exit 1
fi
if ! grep -q '^backend_rollback_status=restored$' \
  "${deploy_state_dir}/releases/failed-backend-health-release/release.env"; then
  echo "FAIL: backend health rollback was not recorded" >&2
  exit 1
fi

reset_runtime_state
printf 'caddy-run-failure-known-good-caddy\n' >"${caddyfile_path}"
cp "${caddyfile_path}" "${known_good_caddyfile}"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAIL_RUN_CONTAINER=scc-caddy \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="failed-caddy-run-release" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: replacement Caddy start failure must fail the deploy" >&2
  exit 1
fi
if [[ "$(cat "${docker_state_dir}/scc-backend.image_ref")" != 'ghcr.io/example/scc-backend:old' || \
  "$(cat "${docker_state_dir}/scc-backend.running")" != 'true' || \
  "$(cat "${docker_state_dir}/scc-caddy.image_ref")" != 'caddy:2-alpine' || \
  "$(cat "${docker_state_dir}/scc-caddy.running")" != 'true' ]]; then
  echo "FAIL: Caddy start failure did not restore prior backend and Caddy containers" >&2
  exit 1
fi
if ! cmp -s "${known_good_caddyfile}" "${caddyfile_path}"; then
  echo "FAIL: Caddy start failure did not restore prior Caddyfile" >&2
  exit 1
fi
if ! grep -q '^caddy_rollback_status=restored$' \
  "${deploy_state_dir}/releases/failed-caddy-run-release/release.env"; then
  echo "FAIL: Caddy start rollback was not recorded" >&2
  exit 1
fi

reset_runtime_state
printf 'caddy-abort-known-good-caddy\n' >"${caddyfile_path}"
cp "${caddyfile_path}" "${known_good_caddyfile}"
caddy_abort_stderr="${tmpdir}/caddy-abort-recovery.stderr"
set +e
PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_LOG="${log_file}" \
FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
FAKE_CURL_LOG="${curl_log}" \
FAKE_TARGET_IMAGE_REF="${immutable_image}" \
FAIL_RENAME_SOURCE=scc-caddy \
FAIL_START_CONTAINER=scc-caddy \
APP_ENV_PATH="${app_env}" \
GHCR_IMAGE="${immutable_image}" \
CADDYFILE_PATH="${caddyfile_path}" \
DEPLOY_STATE_DIR="${deploy_state_dir}" \
RELEASE_ID="failed-caddy-abort-recovery-release" \
MANAGE_CADDY=true \
"${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>"${caddy_abort_stderr}"
caddy_abort_exit=$?
set -e
if [[ "${caddy_abort_exit}" -ne 90 ]] || \
  ! grep -q '^result=rollback_failed$' "${deploy_state_dir}/releases/failed-caddy-abort-recovery-release/result.env" || \
  ! grep -q '^backend_rollback_status=restored$' "${deploy_state_dir}/releases/failed-caddy-abort-recovery-release/release.env" || \
  ! grep -q '^caddy_rollback_status=failed$' "${deploy_state_dir}/releases/failed-caddy-abort-recovery-release/release.env" || \
  ! grep -q '^CRITICAL: deploy failed and rollback did not complete' "${caddy_abort_stderr}" || \
  ! cmp -s "${known_good_caddyfile}" "${caddyfile_path}"; then
  echo "FAIL: original Caddy restart failure was not reported as critical recovery failure" >&2
  exit 1
fi

reset_runtime_state
printf 'public-failure-known-good-caddy\n' >"${caddyfile_path}"
cp "${caddyfile_path}" "${known_good_caddyfile}"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAIL_PUBLIC_API=true \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="failed-public-release" \
  SOURCE_COMMIT="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: public API health failure must fail the deploy" >&2
  exit 1
fi

if [[ "$(cat "${docker_state_dir}/scc-backend.image_ref")" != 'ghcr.io/example/scc-backend:old' || "$(cat "${docker_state_dir}/scc-backend.running")" != 'true' ]]; then
  echo "FAIL: public health failure did not restore the previous backend" >&2
  exit 1
fi

if [[ "$(cat "${docker_state_dir}/scc-caddy.image_ref")" != 'caddy:2-alpine' || "$(cat "${docker_state_dir}/scc-caddy.running")" != 'true' ]]; then
  echo "FAIL: public health failure did not restore the previous Caddy container" >&2
  exit 1
fi

if ! cmp -s "${known_good_caddyfile}" "${caddyfile_path}"; then
  echo "FAIL: public health rollback did not restore the previous Caddyfile" >&2
  exit 1
fi

if [[ -e "${docker_state_dir}/scc-backend-previous" || -e "${docker_state_dir}/scc-caddy-previous" ]]; then
  echo "FAIL: public health rollback left previous containers stranded" >&2
  exit 1
fi

if ! grep -q '^result=failed$' "${deploy_state_dir}/releases/failed-public-release/result.env"; then
  echo "FAIL: public health rollback was not recorded" >&2
  exit 1
fi

reset_runtime_state
printf 'rollback-failure-known-good-caddy\n' >"${caddyfile_path}"
cp "${caddyfile_path}" "${known_good_caddyfile}"
rollback_failure_stderr="${tmpdir}/rollback-failure.stderr"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAIL_PUBLIC_API=true \
  FAIL_RENAME_SOURCE=scc-backend-previous \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="failed-rollback-release" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>"${rollback_failure_stderr}"; then
  echo "FAIL: rollback failure unexpectedly passed the deploy" >&2
  exit 1
fi
if ! grep -q '^result=rollback_failed$' \
  "${deploy_state_dir}/releases/failed-rollback-release/result.env" || \
  ! grep -q '^exit_code=90$' \
  "${deploy_state_dir}/releases/failed-rollback-release/result.env" || \
  ! grep -q '^backend_rollback_status=failed$' \
  "${deploy_state_dir}/releases/failed-rollback-release/release.env" || \
  ! grep -q '^caddy_rollback_status=restored$' \
  "${deploy_state_dir}/releases/failed-rollback-release/release.env" || \
  ! grep -q '^rollback_status=failed$' \
  "${deploy_state_dir}/releases/failed-rollback-release/release.env"; then
  echo "FAIL: rollback failure was recorded as an ordinary deploy failure" >&2
  exit 1
fi
if ! grep -q '^CRITICAL: deploy failed and rollback did not complete' "${rollback_failure_stderr}"; then
  echo "FAIL: rollback failure did not emit a distinct critical log" >&2
  exit 1
fi
if [[ ! -e "${docker_state_dir}/scc-backend-previous" || \
  "$(cat "${docker_state_dir}/scc-caddy.running")" != 'true' ]] || \
  ! cmp -s "${known_good_caddyfile}" "${caddyfile_path}"; then
  echo "FAIL: rollback failure test did not preserve recoverable prior state" >&2
  exit 1
fi

reset_runtime_state
printf 'migration-failure-known-good-caddy\n' >"${caddyfile_path}"
migration_failure_stderr="${tmpdir}/migration-failure.stderr"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAIL_MIGRATION_STEP=up \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="failed-migration-release" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>"${migration_failure_stderr}"; then
  echo "FAIL: migration up failure must fail the deploy" >&2
  exit 1
fi

failed_migration_dir="${deploy_state_dir}/releases/failed-migration-release"
if grep -q -- '--name scc-backend-candidate' "${log_file}"; then
  echo "FAIL: candidate started after migration failure" >&2
  exit 1
fi
if [[ "$(cat "${docker_state_dir}/scc-backend.image_ref")" != 'ghcr.io/example/scc-backend:old' || \
  "$(cat "${docker_state_dir}/scc-backend.running")" != 'true' ]]; then
  echo "FAIL: migration failure changed the current backend" >&2
  exit 1
fi
if ! grep -q '^result=failed$' "${failed_migration_dir}/result.env" || \
  ! grep -q '^migration_failed_step=up$' "${failed_migration_dir}/release.env" || \
  ! grep -q '^migration_up_result=failed$' "${failed_migration_dir}/release.env" || \
  [[ ! -s "${failed_migration_dir}/migration_up.stderr" ]] || \
  [[ -e "${failed_migration_dir}/migration_status_after.json" ]]; then
  echo "FAIL: migration failure evidence is incomplete or later migration gates still ran" >&2
  exit 1
fi
if [[ ! -s "${deploy_state_dir}/predeploy-backups/failed-migration-release.dump" ]]; then
  echo "FAIL: migration started without a retained predeploy dump" >&2
  exit 1
fi
if ! grep -q 'forward migrations may have committed' "${migration_failure_stderr}" || \
  grep -q 'AUTO_MIGRATE' "${migration_failure_stderr}"; then
  echo "FAIL: migration failure guidance is stale or omits forward-schema safety" >&2
  exit 1
fi

reset_runtime_state
printf 'candidate-failure-known-good-caddy\n' >"${caddyfile_path}"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAIL_CANDIDATE_HEALTH=true \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="failed-candidate-release" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: unhealthy candidate must fail the deploy" >&2
  exit 1
fi

if grep -q 'docker stop scc-backend' "${log_file}"; then
  echo "FAIL: unhealthy candidate stopped the current backend" >&2
  exit 1
fi

if [[ "$(cat "${docker_state_dir}/scc-backend.image_ref")" != 'ghcr.io/example/scc-backend:old' || "$(cat "${docker_state_dir}/scc-backend.running")" != 'true' ]]; then
  echo "FAIL: unhealthy candidate changed the current backend" >&2
  exit 1
fi

reset_runtime_state
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAIL_RENAME_SOURCE=scc-backend \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="aborted-switch-release" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: backend rename failure unexpectedly passed the deploy" >&2
  exit 1
fi
if [[ "$(cat "${docker_state_dir}/scc-backend.image_ref")" != 'ghcr.io/example/scc-backend:old' || \
  "$(cat "${docker_state_dir}/scc-backend.running")" != 'true' ]] || \
  ! grep -q '^result=failed$' "${deploy_state_dir}/releases/aborted-switch-release/result.env"; then
  echo "FAIL: aborted backend switch did not restore and verify the original container" >&2
  exit 1
fi

reset_runtime_state
aborted_switch_stderr="${tmpdir}/aborted-switch-recovery.stderr"
set +e
PATH="${bin_dir}:${PATH}" \
FAKE_DOCKER_LOG="${log_file}" \
FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
FAKE_CURL_LOG="${curl_log}" \
FAKE_TARGET_IMAGE_REF="${immutable_image}" \
FAIL_RENAME_SOURCE=scc-backend \
FAIL_START_CONTAINER=scc-backend \
APP_ENV_PATH="${app_env}" \
GHCR_IMAGE="${immutable_image}" \
CADDYFILE_PATH="${caddyfile_path}" \
DEPLOY_STATE_DIR="${deploy_state_dir}" \
RELEASE_ID="failed-switch-recovery-release" \
MANAGE_CADDY=true \
"${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>"${aborted_switch_stderr}"
aborted_switch_exit=$?
set -e
if [[ "${aborted_switch_exit}" -ne 90 ]] || \
  ! grep -q '^result=rollback_failed$' "${deploy_state_dir}/releases/failed-switch-recovery-release/result.env" || \
  ! grep -q '^backend_rollback_status=failed$' "${deploy_state_dir}/releases/failed-switch-recovery-release/release.env" || \
  ! grep -q '^CRITICAL: deploy failed and rollback did not complete' "${aborted_switch_stderr}"; then
  echo "FAIL: original backend restart failure was not reported as critical recovery failure" >&2
  exit 1
fi

reset_runtime_state
printf 'backup-failure-known-good-caddy\n' >"${caddyfile_path}"
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAIL_PGDUMP=true \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="failed-backup-release" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: required predeploy pg_dump failure must stop deploy" >&2
  exit 1
fi

if grep -q -- '--name scc-backend-candidate' "${log_file}"; then
  echo "FAIL: candidate started without a valid required predeploy backup" >&2
  exit 1
fi

reset_runtime_state
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAKE_TARGET_IMAGE_REF="${immutable_image}" \
  FAIL_PGRESTORE_VALIDATION=true \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="invalid-backup-release" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: invalid predeploy custom-format dump must stop deploy" >&2
  exit 1
fi
if grep -q -- '--entrypoint /app/scc-migrate' "${log_file}" || \
  grep -q -- '--name scc-backend-candidate' "${log_file}" || \
  [[ -e "${deploy_state_dir}/predeploy-backups/invalid-backup-release.dump" ]]; then
  echo "FAIL: deploy continued or published a predeploy dump after pg_restore validation failed" >&2
  exit 1
fi

reset_runtime_state
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  FAIL_DEPLOY_LOCK=true \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="failed-lock-release" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: deploy lock contention must stop deploy" >&2
  exit 1
fi

if [[ -s "${log_file}" ]]; then
  echo "FAIL: deploy touched Docker after lock acquisition failed" >&2
  exit 1
fi

if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE="ghcr.io/example/scc-backend:latest" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${deploy_state_dir}" \
  RELEASE_ID="mutable-image-release" \
  MANAGE_CADDY=true \
  SKIP_IMAGE_PULL=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: mutable registry image must be rejected by default" >&2
  exit 1
fi

reset_runtime_state
if PATH="${bin_dir}:${PATH}" \
  FAKE_DOCKER_LOG="${log_file}" \
  FAKE_DOCKER_STATE_DIR="${docker_state_dir}" \
  FAKE_CURL_LOG="${curl_log}" \
  APP_ENV_PATH="${app_env}" \
  GHCR_IMAGE=" malformed ${immutable_image}" \
  CADDYFILE_PATH="${caddyfile_path}" \
  DEPLOY_STATE_DIR="${tmpdir}/malformed-image-deploy" \
  RELEASE_ID="malformed-image-release" \
  MANAGE_CADDY=true \
  "${repo_root}/scripts/deploy-vps.sh" >/dev/null 2>&1; then
  echo "FAIL: malformed digest reference must be rejected" >&2
  exit 1
fi
if [[ -s "${log_file}" ]]; then
  echo "FAIL: malformed immutable image was rejected only after Docker work" >&2
  exit 1
fi

if grep -q "GHCR_TOKEN='" "${repo_root}/.github/workflows/deploy.yml"; then
  echo "FAIL: GHCR_TOKEN is still passed on the remote SSH command line" >&2
  exit 1
fi
if ! grep -q '^export -n GHCR_TOKEN GHCR_USERNAME' "${repo_root}/scripts/deploy-vps.sh"; then
  echo "FAIL: registry credentials must not remain exported to unrelated deploy subprocesses" >&2
  exit 1
fi

if grep -q '^  packages: write$' "${repo_root}/.github/workflows/deploy.yml" || \
  [[ "$(grep -c '^      packages: write$' "${repo_root}/.github/workflows/deploy.yml")" -ne 1 ]] || \
  [[ "$(grep -c '^      packages: read$' "${repo_root}/.github/workflows/deploy.yml")" -ne 1 ]]; then
  echo "FAIL: GHCR write permission must be limited to build while deploy receives read-only package access" >&2
  exit 1
fi

if ! grep -Fq "printf '%q' \"\$2\"" "${repo_root}/.github/workflows/deploy.yml" || \
  grep -Fq "sed \"s/'/" "${repo_root}/.github/workflows/deploy.yml"; then
  echo "FAIL: temporary deploy env values are not shell-escaped safely" >&2
  exit 1
fi

if ! grep -Fq 'mktemp -d /tmp/scc-backend-deploy.XXXXXX' "${repo_root}/.github/workflows/deploy.yml" || \
  grep -Fq ':/tmp/deploy-scc-backend.sh' "${repo_root}/.github/workflows/deploy.yml" || \
  grep -Fq ':/tmp/scc-backend-deploy.env' "${repo_root}/.github/workflows/deploy.yml"; then
  echo "FAIL: workflow deploy artifacts must use a private unpredictable remote directory" >&2
  exit 1
fi

if [[ ! -f "${repo_root}/deploy/known_hosts" || -L "${repo_root}/deploy/known_hosts" ]] || \
  [[ "$(wc -l < "${repo_root}/deploy/known_hosts" | tr -d ' ')" -ne 1 ]] || \
  ! grep -Eq '^103\.117\.151\.158 ssh-ed25519 [A-Za-z0-9+/]+={0,2}$' "${repo_root}/deploy/known_hosts" || \
  ! grep -Fq 'install -m 600 deploy/known_hosts ~/.ssh/known_hosts' "${repo_root}/.github/workflows/deploy.yml" || \
  grep -Fq 'VPS_SSH_KNOWN_HOSTS' "${repo_root}/.github/workflows/deploy.yml" || \
  grep -Fq 'ssh-keyscan' "${repo_root}/.github/workflows/deploy.yml"; then
  echo "FAIL: workflow must use the reviewed repository-pinned VPS ED25519 host key" >&2
  exit 1
fi

if ! grep -Fq 'image: ${{ steps.immutable.outputs.image }}' "${repo_root}/.github/workflows/deploy.yml" || \
  ! grep -Fq 'IMAGE_DIGEST: ${{ steps.build.outputs.digest }}' "${repo_root}/.github/workflows/deploy.yml" || \
  ! grep -Fq '^sha256:[a-f0-9]{64}$' "${repo_root}/.github/workflows/deploy.yml" || \
  ! grep -Fq 'image=${IMAGE_REPOSITORY}@${IMAGE_DIGEST}' "${repo_root}/.github/workflows/deploy.yml"; then
  echo "FAIL: workflow must deploy the immutable build digest" >&2
  exit 1
fi

if ! grep -Fq 'RELEASE_ID: ${{ github.sha }}-${{ github.run_id }}-${{ github.run_attempt }}' "${repo_root}/.github/workflows/deploy.yml"; then
  echo "FAIL: workflow must assign a unique source-linked release ID" >&2
  exit 1
fi

if ! grep -q 'run: go vet ./\.\.\.' "${repo_root}/.github/workflows/deploy.yml" || \
  ! grep -q 'run: go test -race ./\.\.\.' "${repo_root}/.github/workflows/deploy.yml"; then
  echo "FAIL: backend CI must run go vet and the race detector" >&2
  exit 1
fi

if ! grep -Fq -- "- 'release/**'" "${repo_root}/.github/workflows/deploy.yml" || \
  ! grep -Fq "if: github.event_name == 'workflow_dispatch' || (github.event_name == 'push' && github.ref == 'refs/heads/main')" "${repo_root}/.github/workflows/deploy.yml"; then
  echo "FAIL: release branches must run CI without building or deploying the backend image" >&2
  exit 1
fi

if grep -Eq '^[[:space:]]*uses:[[:space:]]+[^[:space:]]+@v[0-9]+' "${repo_root}/.github/workflows/deploy.yml" || \
  grep -Eq '^[[:space:]]*uses:[[:space:]]+[^[:space:]]+@(main|master|latest)([[:space:]]|$)' "${repo_root}/.github/workflows/deploy.yml"; then
  echo "FAIL: backend workflow actions must be pinned to immutable commit SHAs" >&2
  exit 1
fi

if [[ "$(grep -Ec '^FROM [^[:space:]]+@sha256:[a-f0-9]{64}([[:space:]]|$)' "${repo_root}/Dockerfile")" -ne 2 ]]; then
  echo "FAIL: every backend Dockerfile base image must be pinned to an immutable digest" >&2
  exit 1
fi

for release_check in \
  'bash -n scripts/*.sh' \
  'bash scripts/test-deploy-hardening.sh' \
  'bash scripts/test-doctor-vps.sh' \
  'bash scripts/test-backup-recovery.sh' \
  'bash scripts/test-minio-service-account-rotation.sh' \
  'bash scripts/test-provision-vps-swap.sh'; do
  if ! grep -Fq "${release_check}" "${repo_root}/.github/workflows/deploy.yml"; then
    echo "FAIL: backend CI must run release-tooling check: ${release_check}" >&2
    exit 1
  fi
done

if [[ ! -f "${repo_root}/deploy/99-scc-swap.conf" || -L "${repo_root}/deploy/99-scc-swap.conf" ]] || \
  [[ "$(grep -Fxc 'vm.swappiness=10' "${repo_root}/deploy/99-scc-swap.conf")" -ne 1 ]]; then
  echo "FAIL: the reviewed VPS swappiness policy must be a regular versioned file set to 10" >&2
  exit 1
fi

if ! grep -q 'sudo -n systemctl disable --now nginx' "${repo_root}/scripts/deploy-vps.sh" || \
  ! grep -q 'sudo -n pkill -x nginx' "${repo_root}/scripts/deploy-vps.sh"; then
  echo "FAIL: nginx cleanup must use non-interactive sudo" >&2
  exit 1
fi

if ! grep -q 'CADDYFILE_PATH:=/opt/scc-backend/Caddyfile' "${repo_root}/scripts/deploy-vps.sh"; then
  echo "FAIL: managed Caddy must default to a restart-safe host Caddyfile" >&2
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

if grep -q 'mc anonymous set download' "${repo_root}/scripts/deploy-vps.sh" || \
  ! grep -q 'mc anonymous set none' "${repo_root}/scripts/deploy-vps.sh"; then
  echo "FAIL: evidence bucket must explicitly disable anonymous access" >&2
  exit 1
fi

if ! grep -q 'wildcards are forbidden' "${repo_root}/scripts/deploy-vps.sh" || \
  ! grep -q 'MINIO_INTERNAL_ENDPOINT.*MINIO_CONTAINER.*9000' "${repo_root}/scripts/deploy-vps.sh"; then
  echo "FAIL: deploy must require explicit production CORS and internal MinIO routing" >&2
  exit 1
fi

if ! grep -Fq 'for method in ["GET", "PUT", "HEAD"]' "${repo_root}/scripts/deploy-vps.sh"; then
  echo "FAIL: MinIO CORS must be limited to signed browser GET/PUT/HEAD operations" >&2
  exit 1
fi

echo "deploy hardening checks passed"
