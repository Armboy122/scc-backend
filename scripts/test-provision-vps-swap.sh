#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
provision_script="${script_dir}/provision-vps-swap.sh"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

[[ -f "${provision_script}" ]] || fail "provision script is missing"

fake_bin="${tmpdir}/fake-bin"
mkdir -p "${fake_bin}"
cat >"${fake_bin}/fake-command" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail

command_name="${0##*/}"
last_argument=""
for argument in "$@"; do
  last_argument="${argument}"
done

case "${command_name}" in
  fake-id)
    [[ "${1:-}" == "-u" ]] || exit 64
    printf '0\n'
    ;;
  fake-df)
    printf 'Avail\n%s\n' "${FAKE_DF_AVAILABLE_KIB:?}"
    ;;
  fake-dd)
    output=""
    count=""
    for argument in "$@"; do
      case "${argument}" in
        of=*) output="${argument#of=}" ;;
        count=*) count="${argument#count=}" ;;
      esac
    done
    [[ -n "${output}" && "${count}" =~ ^[0-9]+$ ]] || exit 65
    python3 - "${output}" "${count}" <<'PY'
import os, sys
path, count = sys.argv[1], int(sys.argv[2])
with open(path, "r+b") as output:
    output.truncate(count * 1024 * 1024)
    output.flush()
    os.fsync(output.fileno())
PY
    ;;
  fake-blkid)
    python3 - "${last_argument}" <<'PY'
import sys
with open(sys.argv[1], "rb") as source:
    marker = source.read(9)
if marker != b"SCC_SWAP\n":
    raise SystemExit(2)
print("swap")
PY
    ;;
  fake-mkswap)
    printf 'mkswap %s\n' "${last_argument}" >>"${FAKE_SWAP_LOG:?}"
    python3 - "${last_argument}" <<'PY'
import os, sys
with open(sys.argv[1], "r+b") as output:
    output.seek(0)
    output.write(b"SCC_SWAP\n")
    output.flush()
    os.fsync(output.fileno())
PY
    ;;
  fake-swapon)
    printf 'swapon %s\n' "${last_argument}" >>"${FAKE_SWAP_LOG:?}"
    [[ "${FAKE_SWAPON_FAIL:-false}" != "true" ]] || exit 66
    if ! awk -v target="${last_argument}" 'NR > 1 && $1 == target { found = 1 } END { exit(found ? 0 : 1) }' "${FAKE_PROC_SWAPS:?}"; then
      printf '%s file 2097148 0 -2\n' "${last_argument}" >>"${FAKE_PROC_SWAPS}"
    fi
    ;;
  fake-chown|fake-flock)
    :
    ;;
  *)
    echo "unexpected fake command: ${command_name}" >&2
    exit 67
    ;;
esac
FAKE
chmod 0755 "${fake_bin}/fake-command"
for command_name in id df dd blkid mkswap swapon chown flock; do
  ln -s fake-command "${fake_bin}/fake-${command_name}"
done

new_fixture() {
  local name="$1" fixture="${tmpdir}/$1"
  mkdir -p "${fixture}"
  printf 'Filename Type Size Used Priority\n' >"${fixture}/proc-swaps"
  printf '# fixture fstab\n/dev/root / ext4 defaults 0 1\n' >"${fixture}/fstab"
  : >"${fixture}/commands.log"
  printf '%s\n' "${fixture}"
}

run_provision() {
  local fixture="$1" available_kib="${2:-16384}"
  FAKE_DF_AVAILABLE_KIB="${available_kib}" \
  FAKE_PROC_SWAPS="${fixture}/proc-swaps" \
  FAKE_SWAP_LOG="${fixture}/commands.log" \
  SWAP_FILE_PATH="${fixture}/swapfile" \
  SWAP_SIZE_MIB=2 \
  SWAP_FREE_RESERVE_MIB=1 \
  SWAP_FSTAB_PATH="${fixture}/fstab" \
  SWAP_PROC_SWAPS_PATH="${fixture}/proc-swaps" \
  SWAP_LOCK_PATH="${fixture}/provision.lock" \
  SWAP_ID_COMMAND="${fake_bin}/fake-id" \
  SWAP_DF_COMMAND="${fake_bin}/fake-df" \
  SWAP_DD_COMMAND="${fake_bin}/fake-dd" \
  SWAP_BLKID_COMMAND="${fake_bin}/fake-blkid" \
  SWAP_MKSWAP_COMMAND="${fake_bin}/fake-mkswap" \
  SWAP_SWAPON_COMMAND="${fake_bin}/fake-swapon" \
  SWAP_CHOWN_COMMAND="${fake_bin}/fake-chown" \
  SWAP_FLOCK_COMMAND="${fake_bin}/fake-flock" \
  bash "${provision_script}"
}

file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}

file_inode() {
  if stat -f '%i' "$1" >/dev/null 2>&1; then
    stat -f '%i' "$1"
  else
    stat -c '%i' "$1"
  fi
}

log_count() {
  local fixture="$1" prefix="$2"
  awk -v prefix="${prefix}" 'index($0, prefix) == 1 { count++ } END { print count + 0 }' "${fixture}/commands.log"
}

fstab_swap_count() {
  local fixture="$1"
  awk -v target="${fixture}/swapfile" '$0 !~ /^[[:space:]]*#/ && $1 == target { count++ } END { print count + 0 }' "${fixture}/fstab"
}

# A fresh run allocates exactly the configured size, activates it, secures the
# file, and replaces duplicate/malformed fstab entries with one canonical row.
success_fixture="$(new_fixture success)"
chmod 0640 "${success_fixture}/fstab"
printf '%s none swap defaults 0 0\n' "${success_fixture}/swapfile" >>"${success_fixture}/fstab"
printf '%s none swap sw 0 0\n' "${success_fixture}/swapfile" >>"${success_fixture}/fstab"
run_provision "${success_fixture}" >/dev/null
[[ -f "${success_fixture}/swapfile" && ! -L "${success_fixture}/swapfile" ]] || fail "swap file was not created safely"
[[ "$(file_mode "${success_fixture}/swapfile")" == "600" ]] || fail "swap file mode is not 600"
[[ "$(file_mode "${success_fixture}/provision.lock")" == "600" ]] || fail "provisioning lock mode is not 600"
[[ "$(file_mode "${success_fixture}/fstab")" == "640" ]] || fail "atomic fstab update did not preserve file mode"
[[ "$(wc -c <"${success_fixture}/swapfile" | tr -d '[:space:]')" == "$((2 * 1024 * 1024))" ]] || fail "swap file size is incorrect"
[[ "$(fstab_swap_count "${success_fixture}")" == "1" ]] || fail "fstab does not contain exactly one swap entry"
grep -Fqx "${success_fixture}/swapfile none swap sw 0 0" "${success_fixture}/fstab" || fail "fstab entry is not canonical"
[[ "$(log_count "${success_fixture}" mkswap)" == "1" ]] || fail "mkswap was not called exactly once"
[[ "$(log_count "${success_fixture}" swapon)" == "1" ]] || fail "swapon was not called exactly once"

# A second run is a true no-op for the swap file and fstab, and never formats
# or activates the already-active swap file again.
swap_inode_before="$(file_inode "${success_fixture}/swapfile")"
fstab_inode_before="$(file_inode "${success_fixture}/fstab")"
fstab_checksum_before="$(cksum "${success_fixture}/fstab")"
run_provision "${success_fixture}" >/dev/null
[[ "$(file_inode "${success_fixture}/swapfile")" == "${swap_inode_before}" ]] || fail "idempotent run replaced the swap file"
[[ "$(file_inode "${success_fixture}/fstab")" == "${fstab_inode_before}" ]] || fail "idempotent run replaced fstab"
[[ "$(cksum "${success_fixture}/fstab")" == "${fstab_checksum_before}" ]] || fail "idempotent run changed fstab"
[[ "$(log_count "${success_fixture}" mkswap)" == "1" ]] || fail "idempotent run reformatted swap"
[[ "$(log_count "${success_fixture}" swapon)" == "1" ]] || fail "idempotent run reactivated swap"

# Space is checked before any file, fstab, mkswap, or swapon mutation.
space_fixture="$(new_fixture no-space)"
space_fstab_before="$(cksum "${space_fixture}/fstab")"
if space_output="$(run_provision "${space_fixture}" 2048 2>&1)"; then
  fail "insufficient-space provisioning unexpectedly succeeded"
fi
[[ "${space_output}" == *"insufficient disk space"* ]] || fail "insufficient-space error was not explicit"
[[ ! -e "${space_fixture}/swapfile" && ! -L "${space_fixture}/swapfile" ]] || fail "insufficient-space run created a swap path"
[[ "$(cksum "${space_fixture}/fstab")" == "${space_fstab_before}" ]] || fail "insufficient-space run changed fstab"
[[ ! -s "${space_fixture}/commands.log" ]] || fail "insufficient-space run invoked privileged swap commands"

# Existing non-swap files are rejected before chmod, formatting, or content
# changes, even when they are large enough to satisfy the requested size.
file_fixture="$(new_fixture non-swap-file)"
python3 - "${file_fixture}/swapfile" <<'PY'
import sys
with open(sys.argv[1], "wb") as output:
    output.write(b"do-not-overwrite")
    output.truncate(2 * 1024 * 1024)
PY
chmod 0644 "${file_fixture}/swapfile"
file_checksum_before="$(cksum "${file_fixture}/swapfile")"
if file_output="$(run_provision "${file_fixture}" 16384 2>&1)"; then
  fail "existing non-swap file was accepted"
fi
[[ "${file_output}" == *"existing non-swap file"* ]] || fail "non-swap rejection was not explicit"
[[ "$(cksum "${file_fixture}/swapfile")" == "${file_checksum_before}" ]] || fail "existing non-swap content changed"
[[ "$(file_mode "${file_fixture}/swapfile")" == "644" ]] || fail "existing non-swap metadata changed"
[[ ! -s "${file_fixture}/commands.log" ]] || fail "non-swap rejection invoked privileged swap commands"

# A symlink is rejected without touching its target.
symlink_fixture="$(new_fixture symlink)"
printf 'symlink-target-sentinel\n' >"${symlink_fixture}/target"
symlink_target_before="$(cksum "${symlink_fixture}/target")"
ln -s "${symlink_fixture}/target" "${symlink_fixture}/swapfile"
if symlink_output="$(run_provision "${symlink_fixture}" 16384 2>&1)"; then
  fail "swap symlink was accepted"
fi
[[ "${symlink_output}" == *"refusing to replace a symlink"* ]] || fail "symlink rejection was not explicit"
[[ -L "${symlink_fixture}/swapfile" ]] || fail "swap symlink was removed"
[[ "$(cksum "${symlink_fixture}/target")" == "${symlink_target_before}" ]] || fail "symlink target changed"

# If activation fails, a file created by this run is removed and fstab remains
# unchanged, so the next boot cannot reference an inactive partial provision.
activation_fixture="$(new_fixture activation-failure)"
activation_fstab_before="$(cksum "${activation_fixture}/fstab")"
if activation_output="$(FAKE_SWAPON_FAIL=true run_provision "${activation_fixture}" 16384 2>&1)"; then
  fail "swapon failure unexpectedly succeeded"
fi
[[ "${activation_output}" == *"swapon failed"* ]] || fail "swapon failure was not explicit"
[[ ! -e "${activation_fixture}/swapfile" ]] || fail "failed activation left a created swap file behind"
[[ "$(cksum "${activation_fixture}/fstab")" == "${activation_fstab_before}" ]] || fail "failed activation changed fstab"

echo 'PASS: swap provisioning is secure, idempotent, space-gated, and atomically persisted'
