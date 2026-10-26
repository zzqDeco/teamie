#!/usr/bin/env bash
# Private host-only acceptance transport. No service credentials enter argv/output.
set -euo pipefail
umask 077
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=ops/common.sh
source "$script_dir/common.sh"
[[ $# -gt 0 ]] || exit 2
kind=$1
shift
case "$kind" in
  runtime) service=agent-runtime; command=/app/runtimectl; args=(acceptance "$@");;
  task|isolation) service=sandbox; command=/app/sandboxctl; args=("$kind" "$@");;
  *) exit 2;;
esac
exec 9>"$TEAMIE_ROOT/state/deploy.lock"
flock -s -w 600 9
current="$TEAMIE_ROOT/current"
[[ -f $current/release-manifest.json ]] || exit 1
compose_for "$(realpath "$current/recipe")" "$current/images.env"
"${COMPOSE[@]}" exec -T "$service" "$command" "${args[@]}"
