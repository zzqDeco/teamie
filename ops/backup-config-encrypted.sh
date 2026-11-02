#!/usr/bin/env bash
# Encrypted operator backup/restore for protected host configuration (production.env
# including GATEWAY_MASTER_KEY + service credentials, and feature-flags.json).
# Never writes plaintext or encrypted key material inside $TEAMIE_ROOT/backups.
set -euo pipefail
umask 077
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=ops/common.sh
source "$script_dir/common.sh"

mode="encrypt"
out_file=""
in_file=""
out_dir=""
passphrase_file=""
age_recipient=""
age_identity=""
flags_file=${FEATURE_FLAGS_PATH:-$TEAMIE_ROOT/config/feature-flags.json}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --encrypt)
      mode="encrypt"
      shift
      ;;
    --decrypt)
      mode="decrypt"
      shift
      ;;
    --out)
      [[ $# -ge 2 ]] || exit 2
      out_file=$2
      shift 2
      ;;
    --in)
      [[ $# -ge 2 ]] || exit 2
      in_file=$2
      shift 2
      ;;
    --out-dir)
      [[ $# -ge 2 ]] || exit 2
      out_dir=$2
      shift 2
      ;;
    --passphrase-file)
      [[ $# -ge 2 ]] || exit 2
      passphrase_file=$2
      shift 2
      ;;
    --age-recipient)
      [[ $# -ge 2 ]] || exit 2
      age_recipient=$2
      shift 2
      ;;
    --age-identity)
      [[ $# -ge 2 ]] || exit 2
      age_identity=$2
      shift 2
      ;;
    --flags-file)
      [[ $# -ge 2 ]] || exit 2
      flags_file=$2
      shift 2
      ;;
    *)
      echo "Unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

check_private_mode() {
  local target=$1
  local label=$2
  python3 - "$target" "$label" <<'PY'
import os, stat, sys
p, label = sys.argv[1], sys.argv[2]
st = os.lstat(p)
if stat.S_ISLNK(st.st_mode) or not stat.S_ISREG(st.st_mode):
    raise SystemExit(f"{label} must be a regular non-symlink file")
if (st.st_mode & 0o777) != 0o600:
    raise SystemExit(f"{label} must have mode 0600")
PY
}

scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT

if [[ $mode == "encrypt" ]]; then
  [[ -n $out_file ]] || { echo '--out is required for encrypted config backup.' >&2; exit 2; }
  if [[ -n $passphrase_file && -n $age_recipient ]] || [[ -z $passphrase_file && -z $age_recipient ]]; then
    echo 'Specify exactly one of --passphrase-file or --age-recipient.' >&2
    exit 2
  fi
  check_private_mode "$HOST_ENV" "production.env"
  [[ -f $flags_file && ! -L $flags_file ]] || { echo 'feature-flags.json must be an existing regular file.' >&2; exit 1; }

  python3 - "$out_file" "$TEAMIE_ROOT/backups" <<'PY'
import os, pathlib, sys
out_raw, backups_raw = sys.argv[1], sys.argv[2]
out_parent = pathlib.Path(out_raw).expanduser().resolve(strict=False).parent
out_resolved = out_parent / pathlib.Path(out_raw).name
backups_resolved = pathlib.Path(backups_raw).expanduser().resolve(strict=False)
try:
    common = os.path.commonpath([str(out_resolved), str(backups_resolved)])
except ValueError:
    common = ""
if common == str(backups_resolved):
    raise SystemExit("Refusing to write encrypted master-key config inside $TEAMIE_ROOT/backups; store on separate operator path.")
out_parent.mkdir(parents=True, exist_ok=True)
PY

  stage="$scratch/stage"
  mkdir -m 0700 "$stage"
  cp "$HOST_ENV" "$stage/production.env"
  chmod 0600 "$stage/production.env"
  cp "$flags_file" "$stage/feature-flags.json"
  chmod 0600 "$stage/feature-flags.json"

  python3 - "$stage" <<'PY'
import datetime, hashlib, json, pathlib, sys, tarfile
stage = pathlib.Path(sys.argv[1])
env_bytes = (stage / "production.env").read_bytes()
flags_bytes = (stage / "feature-flags.json").read_bytes()
meta = {
    "version": 1,
    "createdAt": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
    "files": {
        "production.env": hashlib.sha256(env_bytes).hexdigest(),
        "feature-flags.json": hashlib.sha256(flags_bytes).hexdigest(),
    },
}
(stage / "config-backup-meta.json").write_text(json.dumps(meta, indent=2, sort_keys=True) + "\n", encoding="utf-8")
with tarfile.open(stage / "payload.tar", mode="w", format=tarfile.PAX_FORMAT) as tf:
    for name in ("config-backup-meta.json", "production.env", "feature-flags.json"):
        p = stage / name
        info = tarfile.TarInfo(name=name)
        info.size = p.stat().st_size
        info.mode = 0o600
        info.uid = 0
        info.gid = 0
        info.uname = ""
        info.gname = ""
        with p.open("rb") as fh:
            tf.addfile(info, fileobj=fh)
PY

  part_out="${out_file}.part.$$"
  rm -f -- "$part_out"
  if [[ -n $age_recipient ]]; then
    age -r "$age_recipient" -o "$part_out" "$stage/payload.tar"
  else
    check_private_mode "$passphrase_file" "passphrase file"
    python3 - "$passphrase_file" <<'PY'
import pathlib, sys
raw = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8").strip()
if len(raw) < 16:
    raise SystemExit("passphrase file must contain at least 16 characters")
PY
    openssl enc -aes-256-cbc -pbkdf2 -iter 600000 -md sha256 -salt \
      -pass "file:$passphrase_file" -in "$stage/payload.tar" -out "$part_out"
  fi
  chmod 0600 "$part_out"
  mv -f "$part_out" "$out_file"
  echo "Encrypted configuration bundle written."
else
  [[ -n $in_file && -n $out_dir ]] || { echo '--in and --out-dir are required for --decrypt.' >&2; exit 2; }
  if [[ -n $passphrase_file && -n $age_identity ]] || [[ -z $passphrase_file && -z $age_identity ]]; then
    echo 'Specify exactly one of --passphrase-file or --age-identity for --decrypt.' >&2
    exit 2
  fi
  [[ -f $in_file && ! -L $in_file ]] || { echo 'Encrypted bundle does not exist.' >&2; exit 1; }

  payload_tar="$scratch/payload.tar"
  if [[ -n $age_identity ]]; then
    check_private_mode "$age_identity" "age identity file"
    age -d -i "$age_identity" -o "$payload_tar" "$in_file"
  else
    check_private_mode "$passphrase_file" "passphrase file"
    openssl enc -d -aes-256-cbc -pbkdf2 -iter 600000 -md sha256 \
      -pass "file:$passphrase_file" -in "$in_file" -out "$payload_tar"
  fi

  python3 - "$payload_tar" "$out_dir" <<'PY'
import hashlib, hmac, json, os, pathlib, stat, sys, tarfile
tar_path = pathlib.Path(sys.argv[1])
target = pathlib.Path(sys.argv[2])
if target.exists() or target.is_symlink():
    st = os.lstat(target)
    if stat.S_ISLNK(st.st_mode) or not stat.S_ISDIR(st.st_mode):
        raise SystemExit("restore out-dir must be a real directory")
    if os.listdir(target):
        raise SystemExit("refusing to decrypt configuration into non-empty directory")
    os.chmod(target, 0o700)
else:
    target.mkdir(parents=True, exist_ok=False, mode=0o700)
    os.chmod(target, 0o700)

allowed = {"config-backup-meta.json", "production.env", "feature-flags.json"}
blobs = {}
with tarfile.open(tar_path, mode="r:") as tf:
    for member in tf:
        if not member.isfile() or member.name not in allowed or member.name in blobs:
            raise SystemExit(f"unexpected member in encrypted config bundle: {member.name!r}")
        if member.size > 1024 * 1024:
            raise SystemExit("config member exceeds 1 MiB safety limit")
        reader = tf.extractfile(member)
        if reader is None:
            raise SystemExit(f"missing content for {member.name}")
        blobs[member.name] = reader.read()

if set(blobs) != allowed:
    raise SystemExit("encrypted config bundle is incomplete")
meta = json.loads(blobs["config-backup-meta.json"].decode("utf-8"))
expected = meta.get("files", {})
for fname, mode in (("production.env", 0o600), ("feature-flags.json", 0o644)):
    actual_hash = hashlib.sha256(blobs[fname]).hexdigest()
    exp_hash = expected.get(fname, "")
    if not exp_hash or not hmac.compare_digest(actual_hash, exp_hash):
        raise SystemExit(f"integrity check failed for {fname}")
    dest = target / fname
    fd = os.open(str(dest), os.O_WRONLY | os.O_CREAT | os.O_EXCL, mode)
    try:
        os.fchmod(fd, mode)
        os.write(fd, blobs[fname])
        os.fsync(fd)
    finally:
        os.close(fd)
PY
  echo "Decrypted configuration bundle verified and restored."
fi
