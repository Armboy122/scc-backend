#!/usr/bin/env bash

# Rotate the API/backup MinIO credential without granting it MinIO admin or
# bucket-policy mutation rights. This script deliberately does not redeploy the
# backend: prepare/finalize bracket the existing atomic deploy-vps.sh flow, and
# rollback/finalize-rollback bracket the same deploy flow in reverse.

set +x
set -euo pipefail
umask 077

: "${APP_ENV_PATH:=/opt/scc-backend/.env}"
: "${ROTATION_STATE_ROOT:=$(dirname -- "${APP_ENV_PATH}")/credential-rotations}"
: "${ROTATION_PENDING_PATH:=${ROTATION_STATE_ROOT}/pending}"
: "${ROTATION_LOCK_PATH:=${ROTATION_STATE_ROOT}/rotation.lock}"
: "${MINIO_CONTAINER:=scc-minio}"
: "${BACKEND_CONTAINER:=scc-backend}"
: "${DOCKER_NETWORK:=scc-net}"
: "${BACKEND_READY_URL:=http://127.0.0.1:8080/api/v1/readyz}"
: "${ROTATION_DOCKER_COMMAND:=docker}"
: "${ROTATION_CURL_COMMAND:=curl}"
: "${ROTATION_OPENSSL_COMMAND:=openssl}"
: "${ROTATION_ALLOW_NON_ROOT:=false}"

MINIO_ROOT_USER=""
MINIO_ROOT_PASSWORD=""
MINIO_ACCESS_KEY=""
MINIO_SECRET_KEY=""
MINIO_BUCKET=""
MINIO_MC_IMAGE=""
LOADED_ENV_KEYS=(__rotation_no_env_key__)
export -n MINIO_ROOT_USER MINIO_ROOT_PASSWORD MINIO_ACCESS_KEY MINIO_SECRET_KEY MINIO_BUCKET MINIO_MC_IMAGE 2>/dev/null || true

pending_rotation_id=""
state_dir=""
policy_path=""
helper_path=""
before_env_path=""
candidate_env_path=""

prepare_account_created=false
prepare_env_installed=false
prepare_cleanup_succeeded=false

die() {
  printf 'ERROR: %s\n' "$1" >&2
  exit 1
}

has_control_character() {
  [[ "$1" == *[[:cntrl:]]* ]]
}

is_safe_identifier() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]
}

is_rotation_id() {
  [[ "$1" =~ ^[0-9]{8}T[0-9]{6}Z-[a-f0-9]{8}$ ]]
}

is_immutable_image_ref() {
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._/:+-]*@sha256:[[:xdigit:]]{64}$ ]]
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

require_protected_regular_file() {
  local path="$1" label="$2" mode permission_digits group_digit other_digit
  [[ "${path}" == /* ]] || die "${label} must be an absolute path"
  has_control_character "${path}" && die "${label} contains a control character"
  [[ -f "${path}" && ! -L "${path}" && -r "${path}" ]] || die "${label} must be a readable regular non-symlink file"
  mode="$(file_mode "${path}")" || die "${label} permissions could not be inspected"
  permission_digits="${mode: -3}"
  group_digit="${permission_digits:1:1}"
  other_digit="${permission_digits:2:1}"
  [[ "${group_digit}" == "0" && "${other_digit}" == "0" ]] || die "${label} must not be accessible by group or other"
}

load_minio_env() {
  local path="$1" raw line key value quote seen_key

  require_protected_regular_file "${path}" "MinIO env"
  MINIO_ROOT_USER=""
  MINIO_ROOT_PASSWORD=""
  MINIO_ACCESS_KEY=""
  MINIO_SECRET_KEY=""
  MINIO_BUCKET=""
  MINIO_MC_IMAGE=""
  LOADED_ENV_KEYS=(__rotation_no_env_key__)

  while IFS= read -r raw || [[ -n "${raw}" ]]; do
    quote=""
    has_control_character "${raw}" && die "MinIO env contains a control character"
    line="${raw#"${raw%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    [[ -z "${line}" || "${line}" == \#* ]] && continue
    if [[ "${line}" =~ ^export[[:space:]]+ ]]; then
      line="${line#export}"
      line="${line#"${line%%[![:space:]]*}"}"
    fi
    [[ "${line}" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || die "MinIO env contains an invalid assignment"
    key="${BASH_REMATCH[1]}"
    value="${BASH_REMATCH[2]}"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    for seen_key in "${LOADED_ENV_KEYS[@]}"; do
      [[ "${seen_key}" != "${key}" ]] || die "MinIO env contains a duplicate key"
    done
    LOADED_ENV_KEYS+=("${key}")

    if [[ "${value}" == \'* || "${value}" == \"* ]]; then
      [[ "${#value}" -ge 2 ]] || die "MinIO env contains an invalid quoted value"
      quote="${value:0:1}"
      [[ "${value: -1}" == "${quote}" ]] || die "MinIO env contains an unterminated quoted value"
      value="${value:1:${#value}-2}"
      [[ "${value}" != *"${quote}"* ]] || die "MinIO env contains an unsupported embedded quote"
    elif [[ "${value}" == *[[:space:]]* ]]; then
      die "MinIO env contains an unquoted whitespace value"
    fi
    has_control_character "${value}" && die "MinIO env value contains a control character"
    if [[ "${quote}" == "\"" && ( "${value}" == *'$'* || "${value}" == *'`'* || "${value}" == *'\'* ) ]]; then
      die "MinIO env double-quoted values cannot contain shell expansion syntax"
    fi
    if [[ -z "${quote}" && ( "${value}" == *'$'* || "${value}" == *'`'* || "${value}" == *'\'* || "${value}" == *';'* || "${value}" == *'&'* || "${value}" == *'|'* || "${value}" == *'<'* || "${value}" == *'>'* || "${value}" == *'('* || "${value}" == *')'* ) ]]; then
      die "MinIO env unquoted value contains unsupported shell syntax"
    fi

    case "${key}" in
      MINIO_ROOT_USER|MINIO_ROOT_PASSWORD|MINIO_ACCESS_KEY|MINIO_SECRET_KEY|MINIO_BUCKET|MINIO_MC_IMAGE)
        printf -v "${key}" '%s' "${value}"
        ;;
    esac
  done <"${path}"

  for key in MINIO_ROOT_USER MINIO_ROOT_PASSWORD MINIO_ACCESS_KEY MINIO_SECRET_KEY MINIO_BUCKET MINIO_MC_IMAGE; do
    [[ -n "${!key}" ]] || die "${key} is required in the protected env"
  done
  [[ "${MINIO_BUCKET}" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] || die "MINIO_BUCKET must be a DNS-style bucket name"
  is_immutable_image_ref "${MINIO_MC_IMAGE}" || die "MINIO_MC_IMAGE must use an immutable sha256 digest reference"
}

ensure_runtime_preconditions() {
  local value required_command
  [[ "${APP_ENV_PATH}" == /* && "${ROTATION_STATE_ROOT}" == /* && "${ROTATION_PENDING_PATH}" == /* && "${ROTATION_LOCK_PATH}" == /* ]] || \
    die "rotation paths must be absolute"
  [[ "${ROTATION_STATE_ROOT}" != / && "${ROTATION_PENDING_PATH}" == "${ROTATION_STATE_ROOT}/pending" && \
    "${ROTATION_LOCK_PATH}" == "${ROTATION_STATE_ROOT}/rotation.lock" ]] || \
    die "pending and lock paths must be the fixed files inside ROTATION_STATE_ROOT"
  for value in "${APP_ENV_PATH}" "${ROTATION_STATE_ROOT}" "${ROTATION_PENDING_PATH}" "${ROTATION_LOCK_PATH}" "${MINIO_CONTAINER}" "${BACKEND_CONTAINER}" "${DOCKER_NETWORK}"; do
    has_control_character "${value}" && die "rotation configuration contains a control character"
  done
  is_safe_identifier "${MINIO_CONTAINER}" || die "MINIO_CONTAINER contains unsupported characters"
  is_safe_identifier "${BACKEND_CONTAINER}" || die "BACKEND_CONTAINER contains unsupported characters"
  is_safe_identifier "${DOCKER_NETWORK}" || die "DOCKER_NETWORK contains unsupported characters"
  if [[ ! "${BACKEND_READY_URL}" =~ ^http://127\.0\.0\.1:([0-9]{1,5})/api/v1/readyz$ ]] || \
    (( 10#${BASH_REMATCH[1]:-0} < 1 || 10#${BASH_REMATCH[1]:-0} > 65535 )); then
    die "BACKEND_READY_URL must be the loopback /api/v1/readyz endpoint with a valid port"
  fi
  if [[ "${ROTATION_ALLOW_NON_ROOT}" != "true" && "${EUID}" -ne 0 ]]; then
    die "run this credential rotation as root"
  fi
  for required_command in "${ROTATION_DOCKER_COMMAND}" "${ROTATION_CURL_COMMAND}" "${ROTATION_OPENSSL_COMMAND}" flock mktemp cmp cp mv chmod; do
    command -v "${required_command}" >/dev/null 2>&1 || die "required command is unavailable: ${required_command}"
  done
  if [[ -e "${ROTATION_STATE_ROOT}" && ( ! -d "${ROTATION_STATE_ROOT}" || -L "${ROTATION_STATE_ROOT}" ) ]]; then
    die "ROTATION_STATE_ROOT must be a real directory, not a symlink"
  fi
  mkdir -p "${ROTATION_STATE_ROOT}"
  chmod 0700 "${ROTATION_STATE_ROOT}"
  exec 9>"${ROTATION_LOCK_PATH}"
  flock -n 9 || die "another MinIO credential rotation is running"
}

write_phase() {
  local phase="$1" tmp
  [[ "${phase}" =~ ^[a-z-]+$ ]] || die "invalid rotation phase"
  tmp="$(mktemp "${state_dir}/.phase.XXXXXX")"
  printf '%s\n' "${phase}" >"${tmp}"
  chmod 0600 "${tmp}"
  mv -f "${tmp}" "${state_dir}/phase"
}

read_phase() {
  local phase
  require_protected_regular_file "${state_dir}/phase" "rotation phase"
  IFS= read -r phase <"${state_dir}/phase"
  [[ "${phase}" =~ ^[a-z-]+$ ]] || die "rotation phase is invalid"
  printf '%s\n' "${phase}"
}

write_pending() {
  local rotation_id="$1" tmp
  tmp="$(mktemp "${ROTATION_STATE_ROOT}/.pending.XXXXXX")"
  printf '%s\n' "${rotation_id}" >"${tmp}"
  chmod 0600 "${tmp}"
  mv -f "${tmp}" "${ROTATION_PENDING_PATH}"
}

load_pending_state() {
  require_protected_regular_file "${ROTATION_PENDING_PATH}" "pending rotation marker"
  IFS= read -r pending_rotation_id <"${ROTATION_PENDING_PATH}"
  is_rotation_id "${pending_rotation_id}" || die "pending rotation marker is invalid"
  state_dir="${ROTATION_STATE_ROOT}/${pending_rotation_id}"
  [[ -d "${state_dir}" && ! -L "${state_dir}" ]] || die "pending rotation state directory is invalid"
  before_env_path="${state_dir}/before.env"
  candidate_env_path="${state_dir}/candidate.env"
  policy_path="${state_dir}/policy.json"
  helper_path="${state_dir}/minio-rotation-helper.sh"
  require_protected_regular_file "${before_env_path}" "pre-rotation env backup"
  require_protected_regular_file "${candidate_env_path}" "candidate env backup"
  require_protected_regular_file "${policy_path}" "service-account policy"
  require_protected_regular_file "${helper_path}" "MinIO rotation helper"
}

atomic_install_env() {
  local source="$1" target_dir target_tmp
  require_protected_regular_file "${source}" "rotation env source"
  require_protected_regular_file "${APP_ENV_PATH}" "application env"
  target_dir="$(dirname -- "${APP_ENV_PATH}")"
  target_tmp="$(mktemp "${target_dir}/.env.minio-rotation.XXXXXX")"
  if ! cp -p "${source}" "${target_tmp}"; then
    rm -f "${target_tmp}"
    return 1
  fi
  chmod --reference="${APP_ENV_PATH}" "${target_tmp}" 2>/dev/null || chmod 0600 "${target_tmp}"
  if command -v chown >/dev/null 2>&1; then
    chown --reference="${APP_ENV_PATH}" "${target_tmp}" 2>/dev/null || true
  fi
  if ! mv -f "${target_tmp}" "${APP_ENV_PATH}"; then
    rm -f "${target_tmp}"
    return 1
  fi
}

write_candidate_env() {
  local source="$1" destination="$2" new_access="$3" new_secret="$4"
  local raw line key access_count=0 secret_count=0
  : >"${destination}"
  chmod 0600 "${destination}"
  while IFS= read -r raw || [[ -n "${raw}" ]]; do
    line="${raw#"${raw%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    if [[ "${line}" =~ ^export[[:space:]]+ ]]; then
      line="${line#export}"
      line="${line#"${line%%[![:space:]]*}"}"
    fi
    key=""
    if [[ "${line}" =~ ^([A-Za-z_][A-Za-z0-9_]*)= ]]; then
      key="${BASH_REMATCH[1]}"
    fi
    case "${key}" in
      MINIO_ACCESS_KEY)
        printf 'MINIO_ACCESS_KEY=%s\n' "${new_access}" >>"${destination}"
        access_count=$((access_count + 1))
        ;;
      MINIO_SECRET_KEY)
        printf 'MINIO_SECRET_KEY=%s\n' "${new_secret}" >>"${destination}"
        secret_count=$((secret_count + 1))
        ;;
      *)
        printf '%s\n' "${raw}" >>"${destination}"
        ;;
    esac
  done <"${source}"
  [[ "${access_count}" -eq 1 && "${secret_count}" -eq 1 ]] || die "application env must contain exactly one MinIO application credential pair"
}

write_policy() {
  local bucket="$1" destination="$2"
  printf '%s\n' \
    '{' \
    '  "Version": "2012-10-17",' \
    '  "Statement": [' \
    '    {' \
    '      "Effect": "Allow",' \
    '      "Action": [' \
    '        "s3:GetBucketLocation",' \
    '        "s3:GetBucketPolicy",' \
    '        "s3:ListBucket"' \
    '      ],' \
    "      \"Resource\": [\"arn:aws:s3:::${bucket}\"]" \
    '    },' \
    '    {' \
    '      "Effect": "Allow",' \
    '      "Action": [' \
    '        "s3:GetObject",' \
    '        "s3:PutObject",' \
    '        "s3:DeleteObject"' \
    '      ],' \
    "      \"Resource\": [\"arn:aws:s3:::${bucket}/evidence/v1/*\"]" \
    '    }' \
    '  ]' \
    '}' >"${destination}"
  chmod 0600 "${destination}"
}

write_mc_helper() {
  local destination="$1"
  # The helper receives all credentials over stdin. Nothing secret is placed
  # in Docker argv, environment variables, logs, or mc's normal output.
  printf '%s\n' \
    '#!/bin/sh' \
    'set -eu' \
    'mode="$1"' \
    'minio_container="$2"' \
    'bucket="$3"' \
    'probe_token="$4"' \
    'IFS= read -r root_user' \
    'IFS= read -r root_password' \
    'IFS= read -r app_access' \
    'IFS= read -r app_secret' \
    'mc alias set root "http://${minio_container}:9000" "${root_user}" "${root_password}" >/dev/null 2>&1' \
    'case "${mode}" in' \
    '  create)' \
    '    mc stat "root/${bucket}" >/dev/null 2>&1' \
    '    mc anonymous set none "root/${bucket}" >/dev/null 2>&1' \
    '    mc admin user svcacct add root "${root_user}" --access-key "${app_access}" --secret-key "${app_secret}" --policy /opt/scc/policy.json >/dev/null 2>&1' \
    '    ;;' \
    '  validate|validate-allowed)' \
    '    mc alias set app "http://${minio_container}:9000" "${app_access}" "${app_secret}" >/dev/null 2>&1' \
    '    allowed_key="evidence/v1/service-account-validation/${probe_token}"' \
    '    denied_key="service-account-validation-denied/${probe_token}"' \
    '    denied_bucket="scc-denied-${probe_token}"' \
    '    cleanup() {' \
    '      mc rm --force "root/${bucket}/${allowed_key}" >/dev/null 2>&1 || true' \
    '      mc rm --force "root/${bucket}/${denied_key}" >/dev/null 2>&1 || true' \
    '      mc anonymous set none "root/${bucket}" >/dev/null 2>&1 || true' \
    '      mc rb --force "root/${denied_bucket}" >/dev/null 2>&1 || true' \
    '    }' \
    '    trap cleanup EXIT HUP INT TERM' \
    '    mc stat "app/${bucket}" >/dev/null 2>&1' \
    '    mc ls "app/${bucket}" >/dev/null 2>&1' \
    '    mc anonymous get "app/${bucket}" >/dev/null 2>&1' \
    '    printf "%s" "scc-minio-service-account-validation" | mc pipe "app/${bucket}/${allowed_key}" >/dev/null 2>&1' \
    '    mc stat "app/${bucket}/${allowed_key}" >/dev/null 2>&1' \
    '    [ "$(mc cat "app/${bucket}/${allowed_key}" 2>/dev/null)" = "scc-minio-service-account-validation" ]' \
    '    mc rm --force "app/${bucket}/${allowed_key}" >/dev/null 2>&1' \
    '    if [ "${mode}" = validate ]; then' \
    '      if printf "%s" denied | mc pipe "app/${bucket}/${denied_key}" >/dev/null 2>&1; then exit 41; fi' \
    '      if mc mb "app/${denied_bucket}" >/dev/null 2>&1; then exit 42; fi' \
    '      if mc anonymous set download "app/${bucket}" >/dev/null 2>&1; then exit 43; fi' \
    '      if mc admin info app >/dev/null 2>&1; then exit 44; fi' \
    '    fi' \
    '    trap - EXIT HUP INT TERM' \
    '    cleanup' \
    '    ;;' \
    '  remove)' \
    '    mc admin user svcacct rm root "${app_access}" >/dev/null 2>&1' \
    '    ;;' \
    '  retire)' \
    '    mc admin user svcacct info root "${app_access}" >/dev/null 2>&1' \
    '    mc admin user svcacct rm root "${app_access}" >/dev/null 2>&1' \
    '    ;;' \
    '  *) exit 64 ;;' \
    'esac' >"${destination}"
  chmod 0700 "${destination}"
}

run_mc_helper() {
  local mode="$1" root_env="$2" app_env="$3"
  local root_user root_password root_bucket root_image app_access app_secret app_bucket app_image

  load_minio_env "${root_env}"
  root_user="${MINIO_ROOT_USER}"
  root_password="${MINIO_ROOT_PASSWORD}"
  root_bucket="${MINIO_BUCKET}"
  root_image="${MINIO_MC_IMAGE}"
  load_minio_env "${app_env}"
  app_access="${MINIO_ACCESS_KEY}"
  app_secret="${MINIO_SECRET_KEY}"
  app_bucket="${MINIO_BUCKET}"
  app_image="${MINIO_MC_IMAGE}"
  [[ "${root_bucket}" == "${app_bucket}" && "${root_image}" == "${app_image}" ]] || die "rotation state changed MinIO bucket or client image"

  if ! {
    printf '%s\n%s\n%s\n%s\n' "${root_user}" "${root_password}" "${app_access}" "${app_secret}" |
      "${ROTATION_DOCKER_COMMAND}" run --rm -i \
        --read-only \
        --cap-drop ALL \
        --security-opt no-new-privileges \
        --network "${DOCKER_NETWORK}" \
        --tmpfs /tmp:rw,nosuid,nodev,size=16m \
        -e MC_CONFIG_DIR=/tmp/mc \
        -v "${policy_path}:/opt/scc/policy.json:ro" \
        -v "${helper_path}:/opt/scc/minio-rotation-helper.sh:ro" \
        --entrypoint /bin/sh \
        "${root_image}" \
        /opt/scc/minio-rotation-helper.sh "${mode}" "${MINIO_CONTAINER}" "${root_bucket}" "${pending_rotation_id#*-}"
  } >/dev/null 2>&1; then
    printf 'ERROR: MinIO service-account %s check failed; no credential value was logged\n' "${mode}" >&2
    return 1
  fi
}

validate_active_backend_env() {
  local expected_env="$1" expected_access expected_secret runtime_dump runtime_access="" runtime_secret=""
  local line access_count=0 secret_count=0
  load_minio_env "${expected_env}"
  expected_access="${MINIO_ACCESS_KEY}"
  expected_secret="${MINIO_SECRET_KEY}"
  if ! runtime_dump="$("${ROTATION_DOCKER_COMMAND}" inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "${BACKEND_CONTAINER}" 2>/dev/null)"; then
    die "active backend container could not be inspected"
  fi
  while IFS= read -r line || [[ -n "${line}" ]]; do
    case "${line}" in
      MINIO_ACCESS_KEY=*)
        runtime_access="${line#MINIO_ACCESS_KEY=}"
        access_count=$((access_count + 1))
        ;;
      MINIO_SECRET_KEY=*)
        runtime_secret="${line#MINIO_SECRET_KEY=}"
        secret_count=$((secret_count + 1))
        ;;
    esac
  done <<<"${runtime_dump}"
  unset runtime_dump
  [[ "${access_count}" -eq 1 && "${secret_count}" -eq 1 ]] || die "active backend has an invalid MinIO credential environment"
  [[ "${runtime_access}" == "${expected_access}" && "${runtime_secret}" == "${expected_secret}" ]] || die "active backend has not loaded the expected MinIO credential pair"
  unset expected_access expected_secret runtime_access runtime_secret
  "${ROTATION_CURL_COMMAND}" --fail --silent --show-error --max-time 10 "${BACKEND_READY_URL}" >/dev/null 2>&1 || \
    die "active backend readiness failed after credential deployment"
}

cleanup_prepare_failure() {
  local exit_code=$?
  trap - EXIT HUP INT TERM
  [[ "${exit_code}" -ne 0 ]] || return 0
  set +e
  if [[ "${prepare_env_installed}" == "true" && -f "${before_env_path}" ]]; then
    atomic_install_env "${before_env_path}"
    [[ "$?" -eq 0 ]] || prepare_cleanup_succeeded=false
  fi
  if [[ "${prepare_account_created}" == "true" && -f "${before_env_path}" && -f "${candidate_env_path}" ]]; then
    run_mc_helper remove "${before_env_path}" "${candidate_env_path}"
    [[ "$?" -eq 0 ]] || prepare_cleanup_succeeded=false
  fi
  if [[ "${prepare_cleanup_succeeded}" == "true" ]]; then
    write_phase aborted >/dev/null 2>&1 || true
    rm -f "${candidate_env_path}" "${ROTATION_PENDING_PATH}"
  else
    write_phase recovery-required >/dev/null 2>&1 || true
    printf 'ERROR: automatic cleanup was incomplete; keep the protected rotation state and follow the recovery runbook\n' >&2
  fi
  exit "${exit_code}"
}

prepare_rotation() {
  local rotation_id new_access new_secret
  [[ ! -e "${ROTATION_PENDING_PATH}" ]] || die "a MinIO credential rotation is already pending"
  load_minio_env "${APP_ENV_PATH}"

  rotation_id="$(date -u +%Y%m%dT%H%M%SZ)-$("${ROTATION_OPENSSL_COMMAND}" rand -hex 4)"
  is_rotation_id "${rotation_id}" || die "could not generate a safe rotation identifier"
  pending_rotation_id="${rotation_id}"
  state_dir="${ROTATION_STATE_ROOT}/${rotation_id}"
  mkdir "${state_dir}"
  chmod 0700 "${state_dir}"
  before_env_path="${state_dir}/before.env"
  candidate_env_path="${state_dir}/candidate.env"
  policy_path="${state_dir}/policy.json"
  helper_path="${state_dir}/minio-rotation-helper.sh"
  write_phase initializing
  write_pending "${rotation_id}"
  prepare_cleanup_succeeded=true
  trap cleanup_prepare_failure EXIT
  trap 'exit 130' HUP INT TERM

  cp -p "${APP_ENV_PATH}" "${before_env_path}"
  chmod 0600 "${before_env_path}"

  new_access="sccapp$("${ROTATION_OPENSSL_COMMAND}" rand -hex 7)"
  new_secret="$("${ROTATION_OPENSSL_COMMAND}" rand -hex 20)"
  [[ "${new_access}" =~ ^sccapp[a-f0-9]{14}$ && "${new_secret}" =~ ^[a-f0-9]{40}$ ]] || die "could not generate safe MinIO service-account credentials"
  write_candidate_env "${before_env_path}" "${candidate_env_path}" "${new_access}" "${new_secret}"
  write_policy "${MINIO_BUCKET}" "${policy_path}"
  write_mc_helper "${helper_path}"

  run_mc_helper create "${before_env_path}" "${candidate_env_path}"
  prepare_account_created=true
  run_mc_helper validate "${before_env_path}" "${candidate_env_path}"
  atomic_install_env "${candidate_env_path}"
  prepare_env_installed=true
  load_minio_env "${APP_ENV_PATH}"
  cmp -s "${APP_ENV_PATH}" "${candidate_env_path}" || die "atomic application env update did not persist exactly"
  write_phase prepared

  unset new_access new_secret
  trap - EXIT HUP INT TERM
  printf 'Prepared MinIO application service-account rotation %s.\n' "${rotation_id}"
  printf 'The protected application env now contains the candidate pair; the running backend is unchanged.\n'
  printf 'Run the normal immutable deploy, then run this script with finalize.\n'
}

finalize_rotation() {
  local phase old_root_user old_root_password old_access old_secret
  load_pending_state
  phase="$(read_phase)"
  [[ "${phase}" == "prepared" ]] || die "finalize requires a prepared rotation, found phase ${phase}"
  cmp -s "${APP_ENV_PATH}" "${candidate_env_path}" || die "application env drifted from the prepared candidate"
  validate_active_backend_env "${candidate_env_path}"
  run_mc_helper validate "${candidate_env_path}" "${candidate_env_path}"

  load_minio_env "${before_env_path}"
  old_root_user="${MINIO_ROOT_USER}"
  old_root_password="${MINIO_ROOT_PASSWORD}"
  old_access="${MINIO_ACCESS_KEY}"
  old_secret="${MINIO_SECRET_KEY}"
  if [[ "${old_access}" != "${old_root_user}" || "${old_secret}" != "${old_root_password}" ]]; then
    run_mc_helper retire "${candidate_env_path}" "${before_env_path}"
  fi
  unset old_root_user old_root_password old_access old_secret
  write_phase finalized
  rm -f "${ROTATION_PENDING_PATH}"
  printf 'Finalized MinIO application service-account rotation %s.\n' "${pending_rotation_id}"
  printf 'The running backend passed readiness and the former non-root service account, if any, was retired.\n'
}

prepare_rollback() {
  local phase old_root_user old_root_password old_access old_secret
  load_pending_state
  phase="$(read_phase)"
  [[ "${phase}" == "prepared" || "${phase}" == "rollback-prepared" ]] || die "rollback requires a prepared rotation, found phase ${phase}"
  load_minio_env "${before_env_path}"
  old_root_user="${MINIO_ROOT_USER}"
  old_root_password="${MINIO_ROOT_PASSWORD}"
  old_access="${MINIO_ACCESS_KEY}"
  old_secret="${MINIO_SECRET_KEY}"
  if [[ "${old_access}" != "${old_root_user}" || "${old_secret}" != "${old_root_password}" ]]; then
    run_mc_helper validate-allowed "${before_env_path}" "${before_env_path}"
  fi
  unset old_root_user old_root_password old_access old_secret
  atomic_install_env "${before_env_path}"
  cmp -s "${APP_ENV_PATH}" "${before_env_path}" || die "rollback env update did not persist exactly"
  write_phase rollback-prepared
  printf 'Restored the protected pre-rotation env for %s.\n' "${pending_rotation_id}"
  printf 'The candidate service account remains valid until the rollback deploy is verified.\n'
  printf 'Run the normal immutable deploy, then run this script with finalize-rollback.\n'
}

finalize_rollback() {
  local phase
  load_pending_state
  phase="$(read_phase)"
  [[ "${phase}" == "rollback-prepared" ]] || die "finalize-rollback requires phase rollback-prepared, found ${phase}"
  cmp -s "${APP_ENV_PATH}" "${before_env_path}" || die "application env drifted from the protected pre-rotation backup"
  validate_active_backend_env "${before_env_path}"
  run_mc_helper validate-allowed "${before_env_path}" "${before_env_path}"
  run_mc_helper retire "${before_env_path}" "${candidate_env_path}"
  write_phase rolled-back
  rm -f "${ROTATION_PENDING_PATH}"
  printf 'Finalized rollback of MinIO credential rotation %s.\n' "${pending_rotation_id}"
  printf 'The running backend passed readiness and the unused candidate service account was retired.\n'
}

show_status() {
  local phase
  if [[ ! -e "${ROTATION_PENDING_PATH}" ]]; then
    printf 'No MinIO credential rotation is pending.\n'
    return 0
  fi
  load_pending_state
  phase="$(read_phase)"
  printf 'Pending MinIO credential rotation: %s\n' "${pending_rotation_id}"
  printf 'Phase: %s\n' "${phase}"
  case "${phase}" in
    prepared)
      printf 'Next action: run the immutable backend deploy, then finalize.\n'
      ;;
    rollback-prepared)
      printf 'Next action: run the immutable backend deploy, then finalize-rollback.\n'
      ;;
    recovery-required)
      printf 'Next action: follow the protected-state recovery section in the runbook.\n'
      ;;
  esac
}

usage() {
  printf '%s\n' \
    'Usage: rotate-minio-service-account.sh COMMAND' \
    '' \
    'Commands:' \
    '  prepare            Create/validate a narrow service account and atomically stage it in .env' \
    '  finalize           Verify deployed credentials/readiness and retire the former service account' \
    '  rollback           Atomically restore the protected pre-rotation .env backup' \
    '  finalize-rollback  Verify the rollback deploy and retire the unused candidate account' \
    '  status             Show the non-secret pending phase' \
    '  help               Show this help'
}

main() {
  local command="${1:-status}"
  [[ "$#" -le 1 ]] || die "only one command is accepted"
  case "${command}" in
    help|-h|--help)
      usage
      return 0
      ;;
    prepare|finalize|rollback|finalize-rollback|status)
      ensure_runtime_preconditions
      ;;
    *)
      usage >&2
      die "unknown command"
      ;;
  esac
  case "${command}" in
    prepare) prepare_rotation ;;
    finalize) finalize_rotation ;;
    rollback) prepare_rollback ;;
    finalize-rollback) finalize_rollback ;;
    status) show_status ;;
  esac
}

main "$@"
