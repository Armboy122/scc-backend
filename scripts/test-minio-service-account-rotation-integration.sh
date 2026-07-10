#!/usr/bin/env bash
set -euo pipefail

# Opt-in real MinIO test. It creates one private scratch bucket and one service
# account in the named local test container, exercises the production rotation
# helper, then removes both. Credentials are accepted only through environment
# variables and are never printed.

: "${SCC_TEST_MINIO_CONTAINER:=}"
: "${SCC_TEST_MINIO_ROOT_USER:=}"
: "${SCC_TEST_MINIO_ROOT_PASSWORD:=}"
: "${SCC_TEST_MINIO_MC_IMAGE:=}"

if [[ -z "${SCC_TEST_MINIO_CONTAINER}" || -z "${SCC_TEST_MINIO_ROOT_USER}" || \
  -z "${SCC_TEST_MINIO_ROOT_PASSWORD}" || -z "${SCC_TEST_MINIO_MC_IMAGE}" ]]; then
  echo 'SKIP: set SCC_TEST_MINIO_CONTAINER, SCC_TEST_MINIO_ROOT_USER, SCC_TEST_MINIO_ROOT_PASSWORD, and SCC_TEST_MINIO_MC_IMAGE'
  exit 0
fi
if [[ ! "${SCC_TEST_MINIO_MC_IMAGE}" =~ ^[^[:space:]@]+@sha256:[[:xdigit:]]{64}$ ]]; then
  echo 'FAIL: SCC_TEST_MINIO_MC_IMAGE must be an immutable digest reference' >&2
  exit 1
fi
if [[ ! "${SCC_TEST_MINIO_CONTAINER}" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ || \
  ! "${SCC_TEST_MINIO_ROOT_USER}" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ || \
  "${SCC_TEST_MINIO_ROOT_PASSWORD}" == *[[:cntrl:]\']* ]]; then
  echo 'FAIL: local test container or root credential contains unsupported characters' >&2
  exit 1
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
rotation_script="${script_dir}/rotate-minio-service-account.sh"
tmpdir="$(mktemp -d)"
network="scc-minio-rotation-$RANDOM-$$"
bucket="scc-rotation-$RANDOM-$$"
candidate_access=""
candidate_secret=""
network_created=false
container_connected=false

admin_helper="${tmpdir}/minio-admin-helper.sh"
cat >"${admin_helper}" <<'SH'
#!/bin/sh
set -eu
mode="$1"
container="$2"
bucket="$3"
IFS= read -r root_user
IFS= read -r root_password
IFS= read -r app_access
IFS= read -r app_secret
mc alias set root "http://${container}:9000" "${root_user}" "${root_password}" >/dev/null 2>&1
case "${mode}" in
  create-bucket)
    mc mb "root/${bucket}" >/dev/null 2>&1
    mc anonymous set none "root/${bucket}" >/dev/null 2>&1
    ;;
  remove-account)
    if mc admin user svcacct info root "${app_access}" >/dev/null 2>&1; then
      mc admin user svcacct rm root "${app_access}" >/dev/null 2>&1
    fi
    ;;
  remove-bucket)
    mc anonymous set none "root/${bucket}" >/dev/null 2>&1 || true
    mc rb --force "root/${bucket}" >/dev/null 2>&1 || true
    ;;
  *) exit 64 ;;
esac
SH
chmod 0700 "${admin_helper}"

run_admin() {
  local mode="$1" access="${2:-unused}" secret="${3:-unused}"
  if ! {
    printf '%s\n%s\n%s\n%s\n' \
      "${SCC_TEST_MINIO_ROOT_USER}" "${SCC_TEST_MINIO_ROOT_PASSWORD}" "${access}" "${secret}" |
      docker run --rm -i \
        --read-only \
        --cap-drop ALL \
        --security-opt no-new-privileges \
        --network "${network}" \
        --tmpfs /tmp:rw,nosuid,nodev,size=16m \
        -e MC_CONFIG_DIR=/tmp/mc \
        -v "${admin_helper}:/opt/scc/minio-admin-helper.sh:ro" \
        --entrypoint /bin/sh \
        "${SCC_TEST_MINIO_MC_IMAGE}" \
        /opt/scc/minio-admin-helper.sh "${mode}" "${SCC_TEST_MINIO_CONTAINER}" "${bucket}"
  } >/dev/null 2>&1; then
    echo "FAIL: real MinIO integration admin step failed: ${mode}" >&2
    return 1
  fi
}

cleanup() {
  local exit_code=$?
  trap - EXIT HUP INT TERM
  set +e
  if [[ -n "${candidate_access}" && "${container_connected}" == true ]]; then
    run_admin remove-account "${candidate_access}" "${candidate_secret}" >/dev/null 2>&1
  fi
  if [[ "${container_connected}" == true ]]; then
    run_admin remove-bucket >/dev/null 2>&1
    docker network disconnect "${network}" "${SCC_TEST_MINIO_CONTAINER}" >/dev/null 2>&1
  fi
  if [[ "${network_created}" == true ]]; then
    docker network rm "${network}" >/dev/null 2>&1
  fi
  rm -rf "${tmpdir}"
  exit "${exit_code}"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM

docker inspect "${SCC_TEST_MINIO_CONTAINER}" >/dev/null 2>&1 || { echo 'FAIL: local MinIO test container is unavailable' >&2; exit 1; }
docker image inspect "${SCC_TEST_MINIO_MC_IMAGE}" >/dev/null 2>&1 || { echo 'FAIL: immutable MinIO mc image is unavailable locally' >&2; exit 1; }
docker network create "${network}" >/dev/null
network_created=true
docker network connect "${network}" "${SCC_TEST_MINIO_CONTAINER}"
container_connected=true
run_admin create-bucket

app_env="${tmpdir}/.env"
cat >"${app_env}" <<ENV
ENV=production
MINIO_ROOT_USER=${SCC_TEST_MINIO_ROOT_USER}
MINIO_ROOT_PASSWORD='${SCC_TEST_MINIO_ROOT_PASSWORD}'
MINIO_ACCESS_KEY=${SCC_TEST_MINIO_ROOT_USER}
MINIO_SECRET_KEY='${SCC_TEST_MINIO_ROOT_PASSWORD}'
MINIO_BUCKET=${bucket}
MINIO_MC_IMAGE=${SCC_TEST_MINIO_MC_IMAGE}
ENV
chmod 0600 "${app_env}"
cp "${app_env}" "${tmpdir}/original.env"

# macOS does not ship flock; the production VPS does. The isolated integration
# test has one process, so this compatibility shim only satisfies the command.
mkdir "${tmpdir}/bin"
cat >"${tmpdir}/bin/flock" <<'SH'
#!/bin/sh
[ "$1" = -n ] && [ "$2" = 9 ]
SH
chmod 0700 "${tmpdir}/bin/flock"

prepare_stdout="${tmpdir}/prepare.stdout"
prepare_stderr="${tmpdir}/prepare.stderr"
APP_ENV_PATH="${app_env}" \
ROTATION_STATE_ROOT="${tmpdir}/rotations" \
ROTATION_DOCKER_COMMAND=docker \
ROTATION_CURL_COMMAND=true \
ROTATION_ALLOW_NON_ROOT=true \
MINIO_CONTAINER="${SCC_TEST_MINIO_CONTAINER}" \
DOCKER_NETWORK="${network}" \
PATH="${tmpdir}/bin:${PATH}" \
"${rotation_script}" prepare >"${prepare_stdout}" 2>"${prepare_stderr}"

pending_id="$(<"${tmpdir}/rotations/pending")"
candidate_env="${tmpdir}/rotations/${pending_id}/candidate.env"
candidate_access="$(sed -n 's/^MINIO_ACCESS_KEY=//p' "${candidate_env}")"
candidate_secret="$(sed -n 's/^MINIO_SECRET_KEY=//p' "${candidate_env}")"
[[ "${candidate_access}" =~ ^sccapp[a-f0-9]{14}$ && "${candidate_secret}" =~ ^[a-f0-9]{40}$ ]] || { echo 'FAIL: real MinIO candidate credential format is invalid' >&2; exit 1; }
if grep -Fq "${SCC_TEST_MINIO_ROOT_PASSWORD}" "${prepare_stdout}" "${prepare_stderr}" || \
  grep -Fq "${candidate_secret}" "${prepare_stdout}" "${prepare_stderr}"; then
  echo 'FAIL: real MinIO rotation printed a credential' >&2
  exit 1
fi

APP_ENV_PATH="${app_env}" \
ROTATION_STATE_ROOT="${tmpdir}/rotations" \
ROTATION_DOCKER_COMMAND=docker \
ROTATION_CURL_COMMAND=true \
ROTATION_ALLOW_NON_ROOT=true \
MINIO_CONTAINER="${SCC_TEST_MINIO_CONTAINER}" \
DOCKER_NETWORK="${network}" \
PATH="${tmpdir}/bin:${PATH}" \
"${rotation_script}" rollback >/dev/null
cmp -s "${app_env}" "${tmpdir}/original.env" || { echo 'FAIL: real MinIO rollback did not restore the exact env' >&2; exit 1; }

run_admin remove-account "${candidate_access}" "${candidate_secret}"
candidate_access=""
candidate_secret=""
echo 'PASS: MinIO 2025 accepted the dedicated service account, exact scoped policy, allowed operations, and explicit denials'
