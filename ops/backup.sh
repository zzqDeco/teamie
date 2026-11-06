#!/usr/bin/env bash
# Local logical PostgreSQL + confined Sandbox filesystem backup.
# Off-host replication and encrypted master-key backup use dedicated scripts.
set -euo pipefail
umask 077
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=ops/common.sh
source "$script_dir/common.sh"

quiesce_agents=${TEAMIE_BACKUP_QUIESCE_AGENTS:-0}
while [[ $# -gt 0 ]]; do
  case "$1" in
    --quiesce-agents)
      quiesce_agents=1
      shift
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 2
      ;;
  esac
done

mkdir -p "$TEAMIE_ROOT/state" "$TEAMIE_ROOT/backups"
if [[ ${TEAMIE_DEPLOY_LOCK_HELD:-0} != 1 ]]; then
  exec 9>"$TEAMIE_ROOT/state/deploy.lock"
  if [[ $quiesce_agents == 1 ]]; then
    flock -w 600 9
  else
    flock -s -w 600 9
  fi
fi
exec 8>"$TEAMIE_ROOT/state/backup.lock"
flock -w 600 8

current="$TEAMIE_ROOT/current"
[[ -f $current/release-manifest.json ]] || { echo 'No deployed release to back up.' >&2; exit 1; }

min_free_bytes=${TEAMIE_BACKUP_MIN_FREE_BYTES:-2147483648}
python3 "$script_dir/sandbox_archive.py" check-space \
  --path "$TEAMIE_ROOT/backups" --min-bytes "$min_free_bytes" >/dev/null

compose_for "$(realpath "$current/recipe")" "$current/images.env"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
destination=$(mktemp -d "$TEAMIE_ROOT/backups/$stamp-XXXXXX")
complete=0
quiesced=0
sandbox_children_quiesced=0
paused_sandbox_containers=()

unpause_sandbox_children() {
  local unpause_rc=0
  if [[ ${#paused_sandbox_containers[@]} -gt 0 ]]; then
    local cid state
    for cid in "${paused_sandbox_containers[@]}"; do
      [[ -n $cid ]] || continue
      if ! docker unpause "$cid" >/dev/null 2>&1; then
        state=$(docker inspect --format '{{.State.Status}}' "$cid" 2>/dev/null || echo missing)
        if [[ $state == paused ]]; then
          echo "Failed to unpause Sandbox execution container: $cid" >&2
          unpause_rc=1
        fi
      fi
    done
    paused_sandbox_containers=()
  fi
  return "$unpause_rc"
}

resume_quiesced_services() {
  local resume_rc=0
  unpause_sandbox_children || resume_rc=1
  if [[ $quiesced == 1 ]]; then
    if "${COMPOSE[@]}" up -d --no-deps --wait --wait-timeout 180 sandbox agent-runtime; then
      quiesced=0
    else
      resume_rc=1
    fi
  fi
  return "$resume_rc"
}

cleanup() {
  local rc=$?
  resume_quiesced_services || rc=1
  if [[ $complete != 1 || $rc -ne 0 ]]; then
    rm -rf -- "$destination"
  fi
  exit "$rc"
}
trap cleanup EXIT

runner_image=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["images"].get("sandbox-runner",""))' "$current/release-manifest.json")
if [[ $quiesce_agents == 1 && -n $runner_image ]]; then
  "${COMPOSE[@]}" stop --timeout 30 agent-runtime sandbox
  quiesced=1

  drain_attempts=${TEAMIE_BACKUP_SANDBOX_DRAIN_ATTEMPTS:-6}
  attempt=0
  while (( attempt < drain_attempts )); do
    running_cids=$(docker ps -q --filter "label=io.teamie.sandbox=true" --filter "status=running")
    if [[ -z $running_cids ]]; then
      break
    fi
    sleep 0.5
    attempt=$((attempt + 1))
  done

  running_cids=$(docker ps -q --filter "label=io.teamie.sandbox=true" --filter "status=running")
  if [[ -n $running_cids ]]; then
    while IFS= read -r cid; do
      [[ -n $cid ]] || continue
      if docker pause "$cid" >/dev/null 2>&1; then
        paused_sandbox_containers+=("$cid")
      else
        state=$(docker inspect --format '{{.State.Status}}' "$cid" 2>/dev/null || echo missing)
        case "$state" in
          paused)
            paused_sandbox_containers+=("$cid")
            ;;
          exited|dead|removing|missing)
            ;;
          *)
            echo "Failed to quiesce running Sandbox execution container $cid (status=$state)." >&2
            exit 1
            ;;
        esac
      fi
    done <<< "$running_cids"
  fi

  still_running=$(docker ps -q --filter "label=io.teamie.sandbox=true" --filter "status=running")
  if [[ -n $still_running ]]; then
    echo "Refusing quiesced backup while Sandbox execution containers remain running: $still_running" >&2
    exit 1
  fi
  sandbox_children_quiesced=1
fi

db_list=$(python3 -c 'import json,sys; print(" ".join("teamie_"+name for name in sorted(json.load(open(sys.argv[1]))["schema"])))' "$current/release-manifest.json")
read -r -a databases <<< "$db_list"
for database in "${databases[@]}"; do
  # Chat's bounded bytea file content and its message relationships are part of
  # this logical database dump, alongside metadata; no attachment volume exists.
  # Expand the protected password inside the PostgreSQL container, not on the host.
  # shellcheck disable=SC2016
  "${COMPOSE[@]}" exec -T postgres sh -ec \
    'PGPASSWORD="$POSTGRES_PASSWORD" exec pg_dump --username postgres --dbname "$1" --format custom' sh "$database" > "$destination/$database.dump.part"
  chmod 0600 "$destination/$database.dump.part"
  mv "$destination/$database.dump.part" "$destination/$database.dump"
  "${COMPOSE[@]}" exec -T postgres pg_restore --list < "$destination/$database.dump" > /dev/null
done

archive_sandbox_root() {
  local src_dir=$1
  local out_name=$2
  local part_file="$destination/$out_name.part"
  if [[ ${EUID:-$(id -u)} -eq 0 || ${TEAMIE_BACKUP_DIRECT_FS:-0} == 1 ]]; then
    python3 "$script_dir/sandbox_archive.py" stream-archive \
      --source "$src_dir" --allow-missing > "$part_file"
  elif [[ -d $src_dir ]]; then
    docker run --rm --network none --read-only --pids-limit 64 --memory 128m \
      --security-opt no-new-privileges:true --cap-drop ALL --cap-add DAC_READ_SEARCH \
      --user 0:0 \
      -v "$script_dir/sandbox_archive.py:/mnt/sandbox_archive.py:ro" \
      -v "$src_dir:/mnt/source:ro" \
      "$runner_image" \
      python3 /mnt/sandbox_archive.py stream-archive --source /mnt/source > "$part_file"
  else
    python3 "$script_dir/sandbox_archive.py" stream-archive \
      --source "$src_dir" --allow-missing > "$part_file"
  fi
  chmod 0600 "$part_file"
  mv "$part_file" "$destination/$out_name"
  python3 "$script_dir/sandbox_archive.py" verify-archive \
    --archive "$destination/$out_name" >/dev/null
}

sandbox_archived=0
if [[ -n $runner_image ]]; then
  archive_sandbox_root "$TEAMIE_ROOT/sandbox-workspaces" "sandbox-workspaces.tar.gz"
  archive_sandbox_root "$TEAMIE_ROOT/sandbox-state" "sandbox-state.tar.gz"
  sandbox_archived=1
fi

cp "$current/release-manifest.json" "$destination/release-manifest.json"
if [[ -f $current/deployment.json ]]; then
  cp "$current/deployment.json" "$destination/deployment.json"
fi

python3 - "$destination/backup-manifest.json" "$stamp" "$sandbox_archived" "$sandbox_children_quiesced" "${databases[@]}" <<'PY'
import datetime, json, os, sys
out_path, stamp, sb_archived, sb_quiesced = sys.argv[1], sys.argv[2], sys.argv[3] == "1", sys.argv[4] == "1"
dbs = sys.argv[5:]
payload = {
    "backupFormatVersion": 2,
    "stamp": stamp,
    "createdAt": datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z"),
    "databases": dbs,
    "sandboxArchived": sb_archived,
    "sandboxQuiesced": sb_quiesced and sb_archived,
}
part = out_path + ".part"
with open(part, "w", encoding="utf-8") as fh:
    json.dump(payload, fh, indent=2, sort_keys=True)
    fh.write("\n")
    fh.flush()
    os.fsync(fh.fileno())
os.chmod(part, 0o600)
os.replace(part, out_path)
PY

python3 "$script_dir/sandbox_archive.py" checksums-write --dir "$destination" >/dev/null
python3 "$script_dir/sandbox_archive.py" checksums-verify --dir "$destination" >/dev/null

if [[ $quiesced == 1 || ${#paused_sandbox_containers[@]} -gt 0 ]]; then
  resume_quiesced_services
fi

complete=1
# Seven full days of complete local backups; never prune incomplete runs as success.
find "$TEAMIE_ROOT/backups" -mindepth 1 -maxdepth 1 -type d -mtime +7 -exec rm -rf -- {} +
echo "Local PostgreSQL and Sandbox backup complete: $stamp"
