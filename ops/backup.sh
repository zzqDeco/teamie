#!/usr/bin/env bash
# Local logical backups only. Off-host copying and restore drills are separate work.
set -euo pipefail
umask 077
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=ops/common.sh
source "$script_dir/common.sh"
if [[ ${TEAMIE_DEPLOY_LOCK_HELD:-0} != 1 ]]; then
  exec 9>"$TEAMIE_ROOT/state/deploy.lock"
  flock -s -w 600 9
fi
exec 8>"$TEAMIE_ROOT/state/backup.lock"
flock -w 600 8
current="$TEAMIE_ROOT/current"
[[ -f $current/release-manifest.json ]] || { echo 'No deployed release to back up.' >&2; exit 1; }
compose_for "$(realpath "$current/recipe")" "$current/images.env"
stamp=$(date -u +%Y%m%dT%H%M%SZ)
destination=$(mktemp -d "$TEAMIE_ROOT/backups/$stamp-XXXXXX")
complete=0
trap '[[ $complete == 1 ]] || rm -rf -- "$destination"' EXIT
for database in teamie_identity teamie_chat; do
  # Expand the protected password inside the PostgreSQL container, not on the host.
  # shellcheck disable=SC2016
  "${COMPOSE[@]}" exec -T postgres sh -ec \
    'PGPASSWORD="$POSTGRES_PASSWORD" exec pg_dump --username postgres --dbname "$1" --format custom' sh "$database" > "$destination/$database.dump.part"
  mv "$destination/$database.dump.part" "$destination/$database.dump"
  "${COMPOSE[@]}" exec -T postgres pg_restore --list < "$destination/$database.dump" > /dev/null
done
cp "$current/release-manifest.json" "$destination/release-manifest.json"
cp "$current/deployment.json" "$destination/deployment.json"
(cd "$destination" && sha256sum teamie_identity.dump teamie_chat.dump > SHA256SUMS)
complete=1
# Seven full days of complete local backups; never prune incomplete runs as success.
find "$TEAMIE_ROOT/backups" -mindepth 1 -maxdepth 1 -type d -mtime +7 -exec rm -rf -- {} +
echo "Local PostgreSQL backup complete: $stamp"
