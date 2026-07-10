#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
rotation_script="${script_dir}/rotate-minio-service-account.sh"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

bin_dir="${tmpdir}/bin"
mkdir -p "${bin_dir}"

cat >"${bin_dir}/openssl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == rand && "${2:-}" == -hex ]]
case "${3:-}" in
  4)
    printf 'deadbeef\n'
    ;;
  7)
    count=0
    [[ ! -f "${FAKE_OPENSSL_COUNTER}" ]] || IFS= read -r count <"${FAKE_OPENSSL_COUNTER}"
    count=$((count + 1))
    printf '%s\n' "${count}" >"${FAKE_OPENSSL_COUNTER}"
    printf '%014x\n' "${count}"
    ;;
  20)
    count=1
    [[ ! -f "${FAKE_OPENSSL_COUNTER}" ]] || IFS= read -r count <"${FAKE_OPENSSL_COUNTER}"
    printf '%040x\n' "${count}"
    ;;
  *) exit 64 ;;
esac
SH
chmod +x "${bin_dir}/openssl"

cat >"${bin_dir}/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[[ "${FAKE_CURL_FAIL:-false}" != true ]]
SH
chmod +x "${bin_dir}/curl"

cat >"${bin_dir}/flock" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == -n && "${2:-}" == 9 ]]
SH
chmod +x "${bin_dir}/flock"

cat >"${bin_dir}/docker" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
: "${FAKE_DOCKER_LOG:?}"
if [[ -n "${MINIO_ROOT_USER+x}" || -n "${MINIO_ROOT_PASSWORD+x}" || \
  -n "${MINIO_ACCESS_KEY+x}" || -n "${MINIO_SECRET_KEY+x}" ]]; then
  printf 'docker leaked-credential-environment\n' >>"${FAKE_DOCKER_LOG}"
  exit 78
fi
printf 'docker %s' "${1:-}" >>"${FAKE_DOCKER_LOG}"
case "${1:-}" in
  run)
    argc=$#
    (( argc >= 5 ))
    eval "mode=\${$((argc - 3))}"
    eval "bucket=\${$((argc - 1))}"
    IFS= read -r root_user
    IFS= read -r root_password
    IFS= read -r app_access
    IFS= read -r app_secret
    printf ' %s\n' "${mode}" >>"${FAKE_DOCKER_LOG}"
    account_path="${FAKE_ACCOUNTS_DIR}/${app_access}"
    case "${mode}" in
      create)
        [[ ! -e "${account_path}" ]]
        printf '%s\n' "${app_secret}" >"${account_path}"
        chmod 0600 "${account_path}"
        ;;
      validate)
        if [[ "${FAKE_VALIDATE_FAIL:-false}" == true ]]; then
          exit 77
        fi
        [[ -f "${account_path}" ]]
        [[ "$(<"${account_path}")" == "${app_secret}" ]]
        ;;
      validate-allowed)
        if [[ "${app_access}" != "${root_user}" || "${app_secret}" != "${root_password}" ]]; then
          [[ -f "${account_path}" ]]
          [[ "$(<"${account_path}")" == "${app_secret}" ]]
        fi
        ;;
      remove|retire)
        [[ -f "${account_path}" ]]
        [[ "$(<"${account_path}")" == "${app_secret}" ]]
        rm -f -- "${account_path}"
        ;;
      *) exit 65 ;;
    esac
    ;;
  inspect)
    printf ' inspect\n' >>"${FAKE_DOCKER_LOG}"
    [[ -f "${FAKE_RUNTIME_ENV}" ]]
    grep -E '^(MINIO_ACCESS_KEY|MINIO_SECRET_KEY)=' "${FAKE_RUNTIME_ENV}"
    ;;
  *) exit 66 ;;
esac
SH
chmod +x "${bin_dir}/docker"

write_env() {
  local destination="$1"
  cat >"${destination}" <<'ENV'
ENV=production
MINIO_ROOT_USER=minio-root
MINIO_ROOT_PASSWORD=minio-root-secret-value
MINIO_ACCESS_KEY=minio-root
MINIO_SECRET_KEY=minio-root-secret-value
MINIO_BUCKET=scc
MINIO_MC_IMAGE=minio/mc@sha256:3333333333333333333333333333333333333333333333333333333333333333
ENV
  chmod 0600 "${destination}"
}

run_rotation() {
  local root="$1"
  shift
  APP_ENV_PATH="${root}/.env" \
  ROTATION_STATE_ROOT="${root}/rotations" \
  ROTATION_DOCKER_COMMAND="${bin_dir}/docker" \
  ROTATION_CURL_COMMAND="${bin_dir}/curl" \
  ROTATION_OPENSSL_COMMAND="${bin_dir}/openssl" \
  ROTATION_ALLOW_NON_ROOT=true \
  FAKE_DOCKER_LOG="${root}/docker.log" \
  FAKE_ACCOUNTS_DIR="${root}/accounts" \
  FAKE_RUNTIME_ENV="${root}/runtime.env" \
  FAKE_OPENSSL_COUNTER="${root}/openssl-counter" \
  FAKE_VALIDATE_FAIL="${FAKE_VALIDATE_FAIL:-false}" \
  MINIO_ROOT_USER=inherited-root-must-not-leak \
  MINIO_ROOT_PASSWORD=inherited-root-secret-must-not-leak \
  MINIO_ACCESS_KEY=inherited-app-must-not-leak \
  MINIO_SECRET_KEY=inherited-app-secret-must-not-leak \
  PATH="${bin_dir}:${PATH}" \
  "${rotation_script}" "$@"
}

fixture="${tmpdir}/success"
mkdir -p "${fixture}/accounts"
: >"${fixture}/docker.log"
write_env "${fixture}/.env"
cp "${fixture}/.env" "${fixture}/original.env"

prepare_stdout="${fixture}/prepare.stdout"
prepare_stderr="${fixture}/prepare.stderr"
if ! run_rotation "${fixture}" prepare >"${prepare_stdout}" 2>"${prepare_stderr}"; then
  sed -n '1,120p' "${prepare_stderr}" >&2
  echo 'FAIL: initial service-account prepare failed' >&2
  exit 1
fi

if grep -Fq 'minio-root-secret-value' "${prepare_stdout}" "${prepare_stderr}" "${fixture}/docker.log" || \
  grep -Eq '[a-f0-9]{40}' "${prepare_stdout}" "${prepare_stderr}" "${fixture}/docker.log"; then
  echo 'FAIL: rotation output or Docker log exposed a credential' >&2
  exit 1
fi
if [[ ! -f "${fixture}/rotations/pending" ]]; then
  sed -n '1,120p' "${prepare_stdout}" >&2
  sed -n '1,120p' "${prepare_stderr}" >&2
  find "${fixture}" -maxdepth 3 -type f -print >&2
  find "${fixture}/rotations" -name phase -type f -exec sed -n '1p' {} \; >&2 2>/dev/null || true
  echo 'FAIL: prepare returned success without a pending marker' >&2
  exit 1
fi
pending_id="$(<"${fixture}/rotations/pending")"
state_dir="${fixture}/rotations/${pending_id}"
cmp -s "${state_dir}/before.env" "${fixture}/original.env" || { echo 'FAIL: protected env backup changed bytes' >&2; exit 1; }
grep -Eq '^MINIO_ACCESS_KEY=sccapp[a-f0-9]{14}$' "${fixture}/.env" || { echo 'FAIL: candidate access key was not staged' >&2; exit 1; }
grep -Eq '^MINIO_SECRET_KEY=[a-f0-9]{40}$' "${fixture}/.env" || { echo 'FAIL: candidate secret was not staged' >&2; exit 1; }
[[ "$(stat -c '%a' "${fixture}/.env" 2>/dev/null || stat -f '%Lp' "${fixture}/.env")" == 600 ]] || { echo 'FAIL: application env mode is not 600' >&2; exit 1; }

for required_action in s3:GetBucketLocation s3:GetBucketPolicy s3:ListBucket s3:GetObject s3:PutObject s3:DeleteObject; do
  grep -Fq "\"${required_action}\"" "${state_dir}/policy.json" || { echo "FAIL: policy omitted ${required_action}" >&2; exit 1; }
done
grep -Fq 'arn:aws:s3:::scc/evidence/v1/*' "${state_dir}/policy.json" || { echo 'FAIL: object policy is not evidence-prefix scoped' >&2; exit 1; }
if grep -Eq 's3:(PutBucketPolicy|CreateBucket|\*)|admin:' "${state_dir}/policy.json"; then
  echo 'FAIL: policy grants bucket mutation, wildcard, or admin access' >&2
  exit 1
fi

# Finalization must not retire anything until the active container has loaded
# the exact candidate pair.
cp "${fixture}/original.env" "${fixture}/runtime.env"
if run_rotation "${fixture}" finalize >/dev/null 2>&1; then
  echo 'FAIL: finalize accepted the pre-rotation backend credential' >&2
  exit 1
fi
candidate_access="$(sed -n 's/^MINIO_ACCESS_KEY=//p' "${fixture}/.env")"
[[ -e "${fixture}/rotations/pending" && -f "${fixture}/accounts/${candidate_access}" ]] || { echo 'FAIL: rejected finalize revoked or lost pending candidate state' >&2; exit 1; }

cp "${fixture}/.env" "${fixture}/runtime.env"
run_rotation "${fixture}" finalize >"${fixture}/finalize.stdout" 2>"${fixture}/finalize.stderr"
[[ ! -e "${fixture}/rotations/pending" ]] || { echo 'FAIL: successful finalize left a pending marker' >&2; exit 1; }
[[ "$(<"${state_dir}/phase")" == finalized ]] || { echo 'FAIL: successful rotation was not finalized' >&2; exit 1; }
first_access="$(sed -n 's/^MINIO_ACCESS_KEY=//p' "${fixture}/.env")"
[[ -f "${fixture}/accounts/${first_access}" ]] || { echo 'FAIL: active service account was removed' >&2; exit 1; }

# A later candidate can be rolled back without revoking the currently active
# account before the old env has been deployed and verified.
run_rotation "${fixture}" prepare >/dev/null
second_id="$(<"${fixture}/rotations/pending")"
second_state="${fixture}/rotations/${second_id}"
second_access="$(sed -n 's/^MINIO_ACCESS_KEY=//p' "${fixture}/.env")"
[[ "${second_access}" != "${first_access}" ]] || { echo 'FAIL: a rotation reused the current access key' >&2; exit 1; }
run_rotation "${fixture}" rollback >/dev/null
[[ -f "${fixture}/accounts/${second_access}" ]] || { echo 'FAIL: rollback revoked candidate before old deploy verification' >&2; exit 1; }
cp "${fixture}/.env" "${fixture}/runtime.env"
run_rotation "${fixture}" finalize-rollback >/dev/null
[[ -f "${fixture}/accounts/${first_access}" && ! -e "${fixture}/accounts/${second_access}" ]] || { echo 'FAIL: rollback account retirement was unsafe' >&2; exit 1; }
[[ "$(<"${second_state}/phase")" == rolled-back ]] || { echo 'FAIL: rollback was not finalized' >&2; exit 1; }

# Permission validation failure must restore the original env, remove the
# unusable service account, and never leak either secret.
failure_fixture="${tmpdir}/failure"
mkdir -p "${failure_fixture}/accounts"
: >"${failure_fixture}/docker.log"
write_env "${failure_fixture}/.env"
cp "${failure_fixture}/.env" "${failure_fixture}/original.env"
if FAKE_VALIDATE_FAIL=true run_rotation "${failure_fixture}" prepare >"${failure_fixture}/stdout" 2>"${failure_fixture}/stderr"; then
  sed -n '1,120p' "${failure_fixture}/docker.log" >&2
  echo 'FAIL: prepare succeeded despite failed least-privilege validation' >&2
  exit 1
fi
cmp -s "${failure_fixture}/.env" "${failure_fixture}/original.env" || { echo 'FAIL: failed prepare did not preserve the original env' >&2; exit 1; }
[[ ! -e "${failure_fixture}/rotations/pending" ]] || { echo 'FAIL: safely aborted prepare left a pending marker' >&2; exit 1; }
[[ -z "$(find "${failure_fixture}/accounts" -type f -print -quit)" ]] || { echo 'FAIL: failed prepare left a service account active' >&2; exit 1; }
if grep -Fq 'minio-root-secret-value' "${failure_fixture}/stdout" "${failure_fixture}/stderr" "${failure_fixture}/docker.log" || \
  grep -Eq '[a-f0-9]{40}' "${failure_fixture}/stdout" "${failure_fixture}/stderr" "${failure_fixture}/docker.log"; then
  echo 'FAIL: failed rotation exposed a credential' >&2
  exit 1
fi

# Target-key duplicates are rejected before any MinIO admin command.
duplicate_fixture="${tmpdir}/duplicate"
mkdir -p "${duplicate_fixture}/accounts"
: >"${duplicate_fixture}/docker.log"
write_env "${duplicate_fixture}/.env"
printf 'MINIO_ACCESS_KEY=duplicate\n' >>"${duplicate_fixture}/.env"
if run_rotation "${duplicate_fixture}" prepare >/dev/null 2>&1; then
  echo 'FAIL: duplicate application credential key was accepted' >&2
  exit 1
fi
[[ ! -s "${duplicate_fixture}/docker.log" ]] || { echo 'FAIL: duplicate env reached Docker/MinIO' >&2; exit 1; }

echo 'PASS: MinIO service-account rotation prepare/finalize/rollback is atomic, redacted, and least-privilege gated'
