#!/usr/bin/env bash
set -euo pipefail

: "${BACKUP_ROOT:=/opt/scc-backend/backups}"
: "${POSTGRES_IMAGE:=}"

backup_dir=""
select_latest=false
freshness_only=false
max_age_seconds=""

usage() {
  echo "usage: $0 (--backup DIR | --latest) [--max-age-seconds N] [--freshness-only]" >&2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --backup)
      [[ $# -ge 2 ]] || { usage; exit 64; }
      backup_dir="$2"
      shift 2
      ;;
    --latest)
      select_latest=true
      shift
      ;;
    --max-age-seconds)
      [[ $# -ge 2 ]] || { usage; exit 64; }
      max_age_seconds="$2"
      shift 2
      ;;
    --freshness-only)
      freshness_only=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      exit 64
      ;;
  esac
done

if [[ "${select_latest}" == "true" && -n "${backup_dir}" || "${select_latest}" != "true" && -z "${backup_dir}" ]]; then
  usage
  exit 64
fi
if [[ -n "${max_age_seconds}" && ! "${max_age_seconds}" =~ ^[0-9]+$ ]]; then
  echo "--max-age-seconds must be a non-negative integer" >&2
  exit 64
fi
command -v python3 >/dev/null 2>&1 || { echo "required command not found: python3" >&2; exit 1; }
if [[ "${freshness_only}" != "true" ]]; then
  command -v docker >/dev/null 2>&1 || { echo "required command not found: docker" >&2; exit 1; }
  if [[ ! "${POSTGRES_IMAGE}" =~ ^[^[:space:]@]+@sha256:[[:xdigit:]]{64}$ ]]; then
    echo "POSTGRES_IMAGE must use an immutable sha256 digest reference for full verification" >&2
    exit 1
  fi
fi

if [[ "${select_latest}" == "true" ]]; then
  backup_dir="$(python3 - "${BACKUP_ROOT}" <<'PY'
import os, re, sys
root = os.path.abspath(sys.argv[1])
pattern = re.compile(r"^\d{8}T\d{6}Z$")
candidates = []
if os.path.isdir(root):
    for name in os.listdir(root):
        path = os.path.join(root, name)
        if pattern.match(name) and os.path.isdir(path) and os.path.isfile(os.path.join(path, ".complete")):
            candidates.append(path)
if not candidates:
    raise SystemExit("no completed SCC backup found")
print(sorted(candidates)[-1])
PY
)"
fi

if [[ "${backup_dir}" != /* || ! -d "${backup_dir}" ]]; then
  echo "backup directory must be an existing absolute path" >&2
  exit 1
fi

python3 - "${backup_dir}" "${max_age_seconds}" "${freshness_only}" <<'PY'
import datetime as dt
import hashlib
import json
import os
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1]).resolve()
max_age = sys.argv[2]
freshness_only = sys.argv[3] == "true"
required = [".complete", "SHA256SUMS.jsonl", "metadata.env", "object-inventory.jsonl", "postgres.dump", "objects"]
for relative in required:
    artifact = root / relative
    if not artifact.exists():
        raise SystemExit(f"required backup artifact missing: {relative}")
    if artifact.is_symlink():
        raise SystemExit(f"required backup artifact must not be a symlink: {relative}")
if not (root / "objects").is_dir():
    raise SystemExit("objects artifact must be a directory")

def read_kv(path):
    result = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        if not raw or "=" not in raw:
            raise SystemExit(f"invalid metadata line in {path.name}")
        key, value = raw.split("=", 1)
        if not re.fullmatch(r"[a-z0-9_]+", key) or key in result:
            raise SystemExit(f"invalid or duplicate metadata key in {path.name}")
        result[key] = value
    return result

complete = read_kv(root / ".complete")
if complete.get("format_version") != "1" or not re.fullmatch(r"[a-f0-9]{64}", complete.get("manifest_sha256", "")):
    raise SystemExit("invalid complete marker")
try:
    dt.datetime.strptime(complete.get("completed_utc", ""), "%Y-%m-%dT%H:%M:%SZ")
except ValueError as error:
    raise SystemExit("invalid completion timestamp") from error
manifest_bytes = (root / "SHA256SUMS.jsonl").read_bytes()
if hashlib.sha256(manifest_bytes).hexdigest() != complete["manifest_sha256"]:
    raise SystemExit("manifest checksum mismatch")

backup_id = root.name
try:
    backup_time = dt.datetime.strptime(backup_id, "%Y%m%dT%H%M%SZ").replace(tzinfo=dt.timezone.utc)
except ValueError as error:
    raise SystemExit("backup directory name is not a UTC backup ID") from error
if max_age:
    age = int((dt.datetime.now(dt.timezone.utc) - backup_time).total_seconds())
    if age < -300:
        raise SystemExit("backup timestamp is more than five minutes in the future")
    age = max(0, age)
    if age > int(max_age):
        raise SystemExit(f"backup is stale: age={age}s max={max_age}s")
if freshness_only:
    raise SystemExit(0)

metadata = read_kv(root / "metadata.env")
if metadata.get("format_version") != "1" or metadata.get("backup_id") != backup_id or metadata.get("storage_scope") != "local-only":
    raise SystemExit("metadata does not match backup directory")
for key in ["postgres_container", "postgres_database", "minio_container", "minio_bucket"]:
    value = metadata.get(key, "")
    if not value or any(ord(character) < 32 or ord(character) == 127 for character in value):
        raise SystemExit(f"metadata identifier is missing or contains control characters: {key}")

manifest = {}
for line_number, raw in enumerate(manifest_bytes.decode("utf-8").splitlines(), 1):
    try:
        item = json.loads(raw)
    except json.JSONDecodeError as error:
        raise SystemExit(f"invalid manifest JSON on line {line_number}") from error
    if set(item) != {"path", "sha256", "size"}:
        raise SystemExit("invalid manifest entry fields")
    relative = item["path"]
    candidate = pathlib.PurePosixPath(relative)
    if candidate.is_absolute() or ".." in candidate.parts or relative in manifest:
        raise SystemExit("unsafe or duplicate manifest path")
    if not re.fullmatch(r"[a-f0-9]{64}", item["sha256"]) or not isinstance(item["size"], int):
        raise SystemExit("invalid manifest digest or size")
    full = root.joinpath(*candidate.parts)
    if full.is_symlink() or not full.is_file():
        raise SystemExit(f"manifest file missing or unsupported: {relative}")
    digest = hashlib.sha256()
    with full.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    if digest.hexdigest() != item["sha256"] or full.stat().st_size != item["size"]:
        raise SystemExit(f"checksum or size mismatch: {relative}")
    manifest[relative] = item

for required_file in ["postgres.dump", "metadata.env", "object-inventory.jsonl"]:
    if required_file not in manifest:
        raise SystemExit(f"required file missing from manifest: {required_file}")

inventory = {}
for line_number, raw in enumerate((root / "object-inventory.jsonl").read_text(encoding="utf-8").splitlines(), 1):
    try:
        item = json.loads(raw)
    except json.JSONDecodeError as error:
        raise SystemExit(f"invalid inventory JSON on line {line_number}") from error
    if set(item) != {"path", "size"} or not isinstance(item["size"], int):
        raise SystemExit("invalid inventory entry")
    relative = item["path"]
    candidate = pathlib.PurePosixPath(relative)
    if candidate.is_absolute() or ".." in candidate.parts or relative in inventory:
        raise SystemExit("unsafe or duplicate inventory path")
    inventory[relative] = item["size"]

actual_objects = {}
for current, dirs, files in os.walk(root / "objects", followlinks=False):
    dirs.sort()
    files.sort()
    for name in dirs:
        if (pathlib.Path(current) / name).is_symlink():
            raise SystemExit("object tree contains a symlink directory")
    for name in files:
        full = pathlib.Path(current) / name
        if full.is_symlink() or not full.is_file():
            raise SystemExit("object tree contains unsupported entry")
        relative = full.relative_to(root / "objects").as_posix()
        actual_objects[relative] = full.stat().st_size
if inventory != actual_objects:
    raise SystemExit("object inventory does not match mirrored object files")
if int(metadata.get("object_count", "-1")) != len(actual_objects):
    raise SystemExit("metadata object count does not match inventory")
if {f"objects/{path}" for path in actual_objects} != {path for path in manifest if path.startswith("objects/")}:
    raise SystemExit("object manifest does not match inventory")

allowed = {".complete", "SHA256SUMS.jsonl", "metadata.env", "object-inventory.jsonl", "postgres.dump", ".offsite-complete", ".offsite-failed"}
for current, dirs, files in os.walk(root, followlinks=False):
    current_path = pathlib.Path(current)
    for name in dirs:
        directory = current_path / name
        if directory.is_symlink():
            raise SystemExit("backup contains a symlink directory")
        relative_directory = directory.relative_to(root).as_posix()
        if current_path == root and relative_directory != "objects":
            raise SystemExit(f"unexpected backup directory: {relative_directory}")
    for name in files:
        full = pathlib.Path(current) / name
        relative = full.relative_to(root).as_posix()
        if not relative.startswith("objects/") and relative not in allowed:
            raise SystemExit(f"unexpected backup artifact: {relative}")
PY

if [[ "${freshness_only}" != "true" ]]; then
  if ! docker run --rm -i \
    --entrypoint pg_restore \
    "${POSTGRES_IMAGE}" \
    --list <"${backup_dir}/postgres.dump" >/dev/null; then
    echo "PostgreSQL dump failed pg_restore validation" >&2
    exit 1
  fi
fi

echo "backup verified: ${backup_dir}"
if [[ -n "${max_age_seconds}" ]]; then
  echo "freshness threshold: ${max_age_seconds}s"
fi
