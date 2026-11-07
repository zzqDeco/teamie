#!/usr/bin/env bash
# Verify local backup freshness + SHA-256 integrity, and optionally replicate the
# latest verified backup directory to off-host storage (rsync/SSH or S3-compatible).
set -euo pipefail
umask 077
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=ops/common.sh
source "$script_dir/common.sh"

max_age_hours=${TEAMIE_BACKUP_MAX_AGE_HOURS:-30}
check_only=0
rsync_dest=""
s3_uri=""
s3_endpoint=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --check-only)
      check_only=1
      shift
      ;;
    --max-age-hours)
      [[ $# -ge 2 ]] || exit 2
      max_age_hours=$2
      shift 2
      ;;
    --rsync-dest)
      [[ $# -ge 2 ]] || exit 2
      rsync_dest=$2
      shift 2
      ;;
    --s3-uri)
      [[ $# -ge 2 ]] || exit 2
      s3_uri=${2%/}
      shift 2
      ;;
    --s3-endpoint-url)
      [[ $# -ge 2 ]] || exit 2
      s3_endpoint=$2
      shift 2
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 2
      ;;
  esac
done

[[ $max_age_hours =~ ^[1-9][0-9]*$ ]] || { echo '--max-age-hours must be a positive integer.' >&2; exit 2; }
if [[ $check_only == 0 && -z $rsync_dest && -z $s3_uri ]]; then
  echo 'Specify --check-only, --rsync-dest <user@host:/path>, or --s3-uri <s3://bucket/prefix>.' >&2
  exit 2
fi

latest_dir=$(python3 - "$TEAMIE_ROOT/backups" "$max_age_hours" <<'PY'
import datetime, os, pathlib, re, stat, sys
backups_root = pathlib.Path(sys.argv[1])
max_age_hours = int(sys.argv[2])
if not backups_root.is_dir() or backups_root.is_symlink():
    raise SystemExit(f"Backups directory missing or invalid: {backups_root}")
stamp_re = re.compile(r"^(\d{8}T\d{6}Z)-[A-Za-z0-9_.-]+$")
candidates = []
for name in os.listdir(backups_root):
    full = backups_root / name
    st = os.lstat(full)
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISDIR(st.st_mode):
        continue
    if not (full / "SHA256SUMS").is_file():
        continue
    m = stamp_re.match(name)
    if m:
        ts = datetime.datetime.strptime(m.group(1), "%Y%m%dT%H%M%SZ").replace(tzinfo=datetime.timezone.utc)
    else:
        ts = datetime.datetime.fromtimestamp(st.st_mtime, tz=datetime.timezone.utc)
    candidates.append((ts, name, full))
if not candidates:
    raise SystemExit(f"No completed backups with SHA256SUMS found in {backups_root}")
candidates.sort(key=lambda item: (item[0], item[1]))
newest_ts, _, newest_path = candidates[-1]
age_seconds = (datetime.datetime.now(datetime.timezone.utc) - newest_ts).total_seconds()
if age_seconds > max_age_hours * 3600:
    raise SystemExit(
        f"Latest backup {newest_path.name} is stale ({age_seconds / 3600:.1f}h old, limit {max_age_hours}h)"
    )
print(str(newest_path))
PY
)

python3 "$script_dir/sandbox_archive.py" checksums-verify --dir "$latest_dir" >/dev/null
backup_name=$(basename -- "$latest_dir")

if [[ $check_only == 1 ]]; then
  echo "Backup health verified: $backup_name"
  exit 0
fi

if [[ -n $rsync_dest ]]; then
  rsync -a --chmod=Du=rwx,Dg=,Do=,Fu=rw,Fg=,Fo= -- "$latest_dir/" "${rsync_dest%/}/$backup_name/"
fi

if [[ -n $s3_uri ]]; then
  aws_args=(s3 sync "$latest_dir" "$s3_uri/$backup_name" --only-show-errors)
  if [[ -n $s3_endpoint ]]; then
    aws_args+=(--endpoint-url "$s3_endpoint")
  fi
  aws "${aws_args[@]}"
fi

echo "Off-host backup sync complete: $backup_name"
