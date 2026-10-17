#!/usr/bin/env bash
# Generate a single-use recovery link through the current Identity container.
set -euo pipefail
umask 077
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
if [[ $# != 2 || $1 != --email || -z $2 ]]; then
  echo 'Usage: ops/password-reset-link.sh --email <account>' >&2
  exit 2
fi
account_email=$2
# shellcheck source=ops/common.sh
source "$script_dir/common.sh"
exec 9>"$TEAMIE_ROOT/state/deploy.lock"
flock -s -w 600 9
current="$TEAMIE_ROOT/current"
[[ -f $current/release-manifest.json ]] || { echo 'No deployed release.' >&2; exit 1; }
python3 - "$script_dir/manifest.py" "$current/release-manifest.json" <<'PY'
import importlib.util,sys
spec=importlib.util.spec_from_file_location('teamie_manifest',sys.argv[1])
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
data=module.validate(module.read(sys.argv[2]))
if data['schema']['identity'] < 3:
    raise SystemExit('The deployed Identity version does not support password recovery.')
PY
compose_for "$(realpath "$current/recipe")" "$current/images.env"
exec "${COMPOSE[@]}" exec -T identity /app/identityctl password-reset-link --email "$account_email"
