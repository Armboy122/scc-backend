#!/usr/bin/env bash
set -euo pipefail

: "${SWAP_FILE_PATH:=/swapfile}"
: "${SWAP_SIZE_MIB:=1024}"
: "${SWAP_FREE_RESERVE_MIB:=256}"
: "${SWAP_FSTAB_PATH:=/etc/fstab}"
: "${SWAP_PROC_SWAPS_PATH:=/proc/swaps}"
: "${SWAP_LOCK_PATH:=/run/lock/scc-provision-swap.lock}"

# Command overrides keep the production path simple while allowing the test
# suite to exercise every privileged/Linux-specific operation deterministically.
: "${SWAP_ID_COMMAND:=id}"
: "${SWAP_DF_COMMAND:=df}"
: "${SWAP_DD_COMMAND:=dd}"
: "${SWAP_BLKID_COMMAND:=blkid}"
: "${SWAP_MKSWAP_COMMAND:=mkswap}"
: "${SWAP_SWAPON_COMMAND:=swapon}"
: "${SWAP_CHOWN_COMMAND:=chown}"
: "${SWAP_FLOCK_COMMAND:=flock}"

umask 077

staged_swap=""
fstab_tmp=""
remove_created_swap_on_failure=false

cleanup() {
  if [[ -n "${staged_swap}" && -f "${staged_swap}" && ! -L "${staged_swap}" ]]; then
    rm -f "${staged_swap}"
  fi
  if [[ -n "${fstab_tmp}" && -f "${fstab_tmp}" && ! -L "${fstab_tmp}" ]]; then
    rm -f "${fstab_tmp}"
  fi
  if [[ "${remove_created_swap_on_failure}" == "true" && -f "${SWAP_FILE_PATH}" && ! -L "${SWAP_FILE_PATH}" ]]; then
    rm -f "${SWAP_FILE_PATH}"
  fi
}
trap cleanup EXIT

die() {
  echo "swap provisioning failed: $*" >&2
  exit 1
}

require_command() {
  local command_path="$1"
  if [[ "${command_path}" == */* ]]; then
    [[ -x "${command_path}" && ! -d "${command_path}" ]] || die "required command is not executable: ${command_path}"
    return
  fi
  command -v "${command_path}" >/dev/null 2>&1 || die "required command not found: ${command_path}"
}

validate_absolute_path() {
  local label="$1" value="$2" base
  if [[ "${value}" != /* || "${value}" == "/" || ! "${value}" =~ ^/[A-Za-z0-9._/-]+$ ]]; then
    die "${label} must be a safe absolute path without whitespace"
  fi
  if [[ "${value}" == *"//"* || "${value}" == *"/./"* || "${value}" == *"/../"* || "${value}" == */. || "${value}" == */.. ]]; then
    die "${label} must not contain empty, current-directory, or parent-directory components"
  fi
  base="${value##*/}"
  [[ -n "${base}" && "${base}" != "." && "${base}" != ".." ]] || die "${label} has an invalid basename"
}

is_non_negative_integer() {
  [[ "$1" =~ ^(0|[1-9][0-9]*)$ ]]
}

file_size_bytes() {
  local size
  size="$(LC_ALL=C wc -c <"$1")"
  size="${size//[[:space:]]/}"
  [[ "${size}" =~ ^[0-9]+$ ]] || die "could not determine the size of $1"
  printf '%s\n' "${size}"
}

is_swap_active() {
  [[ -r "${SWAP_PROC_SWAPS_PATH}" && -f "${SWAP_PROC_SWAPS_PATH}" && ! -L "${SWAP_PROC_SWAPS_PATH}" ]] ||
    die "SWAP_PROC_SWAPS_PATH must be a readable regular non-symlink file"
  awk -v target="${SWAP_FILE_PATH}" 'NR > 1 && $1 == target { found = 1 } END { exit(found ? 0 : 1) }' "${SWAP_PROC_SWAPS_PATH}"
}

validate_fstab_target() {
  if [[ -L "${SWAP_FSTAB_PATH}" ]]; then
    die "refusing to update a symlink at SWAP_FSTAB_PATH"
  fi
  [[ -f "${SWAP_FSTAB_PATH}" && -r "${SWAP_FSTAB_PATH}" && -w "${SWAP_FSTAB_PATH}" ]] ||
    die "SWAP_FSTAB_PATH must be an existing readable and writable regular file"
}

ensure_fstab_entry() {
  local counts matching canonical fstab_dir
  validate_fstab_target

  counts="$(awk -v target="${SWAP_FILE_PATH}" '
    $0 !~ /^[[:space:]]*#/ && $1 == target {
      matching++
      if (NF == 6 && $2 == "none" && $3 == "swap" && $4 == "sw" && $5 == "0" && $6 == "0") {
        canonical++
      }
    }
    END { printf "%d %d\n", matching + 0, canonical + 0 }
  ' "${SWAP_FSTAB_PATH}")"
  matching="${counts%% *}"
  canonical="${counts##* }"
  if [[ "${matching}" == "1" && "${canonical}" == "1" ]]; then
    return
  fi

  fstab_dir="$(dirname "${SWAP_FSTAB_PATH}")"
  [[ -d "${fstab_dir}" && ! -L "${fstab_dir}" ]] || die "SWAP_FSTAB_PATH parent must be a non-symlink directory"
  fstab_tmp="$(mktemp "${fstab_dir}/.scc-fstab.XXXXXX")"
  cp -p "${SWAP_FSTAB_PATH}" "${fstab_tmp}"
  if ! awk -v target="${SWAP_FILE_PATH}" '
    $0 !~ /^[[:space:]]*#/ && $1 == target { next }
    { print }
    END { print target " none swap sw 0 0" }
  ' "${SWAP_FSTAB_PATH}" >"${fstab_tmp}"; then
    die "could not stage the fstab update"
  fi
  if cmp -s "${fstab_tmp}" "${SWAP_FSTAB_PATH}"; then
    rm -f "${fstab_tmp}"
    fstab_tmp=""
    return
  fi
  mv -f "${fstab_tmp}" "${SWAP_FSTAB_PATH}"
  fstab_tmp=""
}

for required_command in \
  "${SWAP_ID_COMMAND}" "${SWAP_DF_COMMAND}" "${SWAP_DD_COMMAND}" "${SWAP_BLKID_COMMAND}" \
  "${SWAP_MKSWAP_COMMAND}" "${SWAP_SWAPON_COMMAND}" "${SWAP_CHOWN_COMMAND}" "${SWAP_FLOCK_COMMAND}" \
  awk chmod cmp cp dirname ln mktemp mv rm wc; do
  require_command "${required_command}"
done

validate_absolute_path SWAP_FILE_PATH "${SWAP_FILE_PATH}"
validate_absolute_path SWAP_FSTAB_PATH "${SWAP_FSTAB_PATH}"
validate_absolute_path SWAP_PROC_SWAPS_PATH "${SWAP_PROC_SWAPS_PATH}"
validate_absolute_path SWAP_LOCK_PATH "${SWAP_LOCK_PATH}"

is_non_negative_integer "${SWAP_SIZE_MIB}" || die "SWAP_SIZE_MIB must be a positive integer"
is_non_negative_integer "${SWAP_FREE_RESERVE_MIB}" || die "SWAP_FREE_RESERVE_MIB must be a non-negative integer"
[[ "${#SWAP_SIZE_MIB}" -le 7 ]] || die "SWAP_SIZE_MIB must be between 1 and 1048576"
[[ "${#SWAP_FREE_RESERVE_MIB}" -le 7 ]] || die "SWAP_FREE_RESERVE_MIB must not exceed 1048576"
(( SWAP_SIZE_MIB > 0 && SWAP_SIZE_MIB <= 1048576 )) || die "SWAP_SIZE_MIB must be between 1 and 1048576"
(( SWAP_FREE_RESERVE_MIB <= 1048576 )) || die "SWAP_FREE_RESERVE_MIB must not exceed 1048576"

uid="$("${SWAP_ID_COMMAND}" -u)"
[[ "${uid}" == "0" ]] || die "this script must run as root"

lock_dir="$(dirname "${SWAP_LOCK_PATH}")"
[[ -d "${lock_dir}" && ! -L "${lock_dir}" ]] || die "SWAP_LOCK_PATH parent must be a non-symlink directory"
if [[ -L "${SWAP_LOCK_PATH}" || ( -e "${SWAP_LOCK_PATH}" && ! -f "${SWAP_LOCK_PATH}" ) ]]; then
  die "SWAP_LOCK_PATH must not be a symlink or non-regular file"
fi
exec 9>>"${SWAP_LOCK_PATH}"
chmod 0600 "${SWAP_LOCK_PATH}"
"${SWAP_FLOCK_COMMAND}" -x 9

validate_fstab_target

swap_dir="$(dirname "${SWAP_FILE_PATH}")"
[[ -d "${swap_dir}" && ! -L "${swap_dir}" ]] || die "SWAP_FILE_PATH parent must be a non-symlink directory"

if [[ -L "${SWAP_FILE_PATH}" ]]; then
  die "refusing to replace a symlink at SWAP_FILE_PATH"
fi
if [[ -e "${SWAP_FILE_PATH}" ]]; then
  [[ -f "${SWAP_FILE_PATH}" ]] || die "refusing to replace a non-regular object at SWAP_FILE_PATH"
  swap_type=""
  if ! swap_type="$("${SWAP_BLKID_COMMAND}" -p -s TYPE -o value "${SWAP_FILE_PATH}" 2>/dev/null)" || [[ "${swap_type}" != "swap" ]]; then
    die "refusing to overwrite an existing non-swap file at SWAP_FILE_PATH"
  fi
  existing_size_bytes="$(file_size_bytes "${SWAP_FILE_PATH}")"
  minimum_size_bytes=$((SWAP_SIZE_MIB * 1024 * 1024))
  (( existing_size_bytes >= minimum_size_bytes )) ||
    die "existing swap file is smaller than SWAP_SIZE_MIB; refusing an in-place resize"
else
  available_kib="$("${SWAP_DF_COMMAND}" --output=avail -k "${swap_dir}" | awk 'NR == 2 { print $1; exit }')"
  [[ "${available_kib}" =~ ^[0-9]+$ ]] || die "could not determine available disk space"
  required_kib=$(((SWAP_SIZE_MIB + SWAP_FREE_RESERVE_MIB) * 1024))
  (( available_kib >= required_kib )) ||
    die "insufficient disk space: need ${required_kib} KiB including reserve, have ${available_kib} KiB"

  staged_swap="$(mktemp "${swap_dir}/.scc-swap.XXXXXX")"
  expected_size_bytes=$((SWAP_SIZE_MIB * 1024 * 1024))
  if ! "${SWAP_DD_COMMAND}" if=/dev/zero "of=${staged_swap}" bs=1M "count=${SWAP_SIZE_MIB}" conv=fsync status=none; then
    die "could not allocate the swap file"
  fi
  actual_size_bytes="$(file_size_bytes "${staged_swap}")"
  [[ "${actual_size_bytes}" == "${expected_size_bytes}" ]] || die "allocated swap file has an unexpected size"
  "${SWAP_CHOWN_COMMAND}" 0:0 "${staged_swap}"
  chmod 0600 "${staged_swap}"
  if ! "${SWAP_MKSWAP_COMMAND}" "${staged_swap}" >/dev/null; then
    die "mkswap failed"
  fi
  staged_swap_type=""
  if ! staged_swap_type="$("${SWAP_BLKID_COMMAND}" -p -s TYPE -o value "${staged_swap}" 2>/dev/null)" || [[ "${staged_swap_type}" != "swap" ]]; then
    die "mkswap did not produce a verifiable swap signature"
  fi
  if ! ln "${staged_swap}" "${SWAP_FILE_PATH}"; then
    die "SWAP_FILE_PATH appeared while provisioning; refusing to overwrite it"
  fi
  rm -f "${staged_swap}"
  staged_swap=""
  remove_created_swap_on_failure=true
fi

"${SWAP_CHOWN_COMMAND}" 0:0 "${SWAP_FILE_PATH}"
chmod 0600 "${SWAP_FILE_PATH}"

if ! is_swap_active; then
  if ! "${SWAP_SWAPON_COMMAND}" -- "${SWAP_FILE_PATH}"; then
    if is_swap_active; then
      remove_created_swap_on_failure=false
    fi
    die "swapon failed"
  fi
fi
is_swap_active || die "swap file is not active after swapon"
remove_created_swap_on_failure=false

ensure_fstab_entry

printf 'Swap is active and persisted: %s (%s MiB minimum)\n' "${SWAP_FILE_PATH}" "${SWAP_SIZE_MIB}"
