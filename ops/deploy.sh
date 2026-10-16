#!/usr/bin/env bash
# apply BUNDLE_DIR GHCR_USER, or rollback vX.Y.Z EXPECTED_CURRENT_SHA GHCR_USER.
# The short-lived registry token is read from stdin, never an argument or env file.
set -euo pipefail
umask 077
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=ops/common.sh
source "$script_dir/common.sh"
mode=${1:-}
[[ $mode == apply || $mode == rollback ]] || { echo 'Expected apply or rollback.' >&2; exit 1; }
exec 9>"$TEAMIE_ROOT/state/deploy.lock"
flock -w 600 9 || { echo 'Another deployment holds the production lock.' >&2; exit 1; }
current="$TEAMIE_ROOT/current"
manifest_tool="$script_dir/manifest.py"
checks=()
if [[ -f $current/release-manifest.json ]]; then
  checks+=(--current "$current/release-manifest.json")
fi
if [[ $mode == apply ]]; then
  [[ $# -eq 3 ]] || exit 1
  recipe=$(realpath "$2")
  [[ $recipe == "$TEAMIE_ROOT/releases/"* ]] || { echo 'Release recipe must be an uploaded host bundle.' >&2; exit 1; }
  manifest="$recipe/release-manifest.json"
  registry_user=$3
  [[ ! -f $TEAMIE_ROOT/state/highwater.json ]] || checks+=(--highwater "$TEAMIE_ROOT/state/highwater.json")
else
  [[ $# -eq 4 && $2 =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ && $3 =~ ^[0-9a-f]{40}$ ]] || exit 1
  [[ -f $current/release-manifest.json ]] || { echo 'No current deployment to roll back.' >&2; exit 1; }
  active_sha=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["source"]["sha"])' "$current/release-manifest.json")
  [[ $active_sha == "$3" ]] || { echo 'Current production SHA changed; re-check before rolling back.' >&2; exit 1; }
  recipe=$(realpath "$current/recipe")
  # Only manifests from successful deployments are eligible, never arbitrary input.
  manifest="$TEAMIE_ROOT/state/history/$2.json"
  registry_user=$4
  checks+=(--rollback)
fi
[[ $registry_user =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] || exit 1
python3 "$manifest_tool" check "$manifest" "${checks[@]}"
[[ $(stat -c '%u' "$HOST_ENV") == "$(id -u)" ]] || { echo 'production.env must belong to the delivery user.' >&2; exit 1; }
scratch=$(mktemp -d "$TEAMIE_ROOT/state/deploy-XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
python3 "$manifest_tool" env "$manifest" > "$scratch/images.env"
compose_for "$recipe" "$scratch/images.env"
# Render secrets only to this private temporary file, never to action logs.
"${COMPOSE[@]}" config --format json > "$scratch/compose.json"
origin=$(python3 "$manifest_tool" config "$scratch/compose.json" "$HOST_ENV")
python3 - "$scratch/compose.json" <<'PY'
import ipaddress,json,pathlib,sys
from urllib.parse import urlsplit
c=json.load(open(sys.argv[1]))
flags=next(v["source"] for v in c["services"]["identity"]["volumes"] if v["target"]=="/etc/teamie/feature-flags.json")
f=json.load(open(flags))
assert isinstance(f.get("revision"),str) and f["revision"], "flags revision required"
assert isinstance(f.get("flags"),dict) and all(type(v) is bool for v in f["flags"].values()), "flags must be booleans"
tls=next(v["source"] for v in c["services"]["caddy"]["volumes"] if v["target"]=="/etc/caddy/issuer.caddy")
text=pathlib.Path(tls).read_text()
assert "tls internal" not in text, "production requires publicly trusted TLS"
host=urlsplit(c["services"]["identity"]["environment"]["APP_ORIGIN"]).hostname
try: ipaddress.ip_address(host)
except ValueError: pass
else:
    assert ("issuer acme" in text and "profile shortlived" in text and "https://acme-v02.api.letsencrypt.org/directory" in text) or "tls /run/tls/fullchain.pem /run/tls/privkey.pem" in text, "IP HTTPS requires explicit shortlived ACME or provided certificates"
PY
export DOCKER_CONFIG="$scratch/docker"
mkdir "$DOCKER_CONFIG"
IFS= read -r registry_token
[[ -n $registry_token ]] || { echo 'Registry token was not supplied on stdin.' >&2; exit 1; }
printf '%s' "$registry_token" | docker login ghcr.io --username "$registry_user" --password-stdin >/dev/null
unset registry_token
"${COMPOSE[@]}" pull --quiet
# Already-pulled immutable digests do not require persisted registry credentials.
rm -rf -- "$DOCKER_CONFIG"
unset DOCKER_CONFIG

if [[ -f $current/release-manifest.json ]]; then
  TEAMIE_DEPLOY_LOCK_HELD=1 "$current/recipe/ops/backup.sh"
fi

restore_previous() {
  if [[ -f $current/release-manifest.json ]] && python3 - "$manifest" "$current/release-manifest.json" "$manifest_tool" <<'PY'
import importlib.util,json,sys
s=importlib.util.spec_from_file_location("manifest",sys.argv[3]); m=importlib.util.module_from_spec(s); s.loader.exec_module(m)
m.compatible(json.load(open(sys.argv[1])),json.load(open(sys.argv[2])))
PY
  then
    echo 'Rollout failed; restoring the previous compatible container images.' >&2
    compose_for "$(realpath "$current/recipe")" "$current/images.env"
    rollout || echo 'Automatic image restoration failed; production needs operator attention.' >&2
  else
    echo 'Rollout failed. Current manifest remains unchanged; schema compatibility prevents automatic image restoration.' >&2
  fi
}

if ! rollout; then restore_previous; exit 1; fi
# Check the migration ledgers against release metadata before considering rollback safe.
for database in identity chat; do
  expected_schema=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["schema"][sys.argv[2]])' "$manifest" "$database")
  # shellcheck disable=SC2016
  actual_schema=$("${COMPOSE[@]}" exec -T postgres sh -ec \
    'PGPASSWORD="$POSTGRES_PASSWORD" exec psql --username postgres --dbname "$1" --tuples-only --no-align --set ON_ERROR_STOP=1 --command "SELECT max(version) FROM $2"' \
    sh "teamie_$database" "${database}_schema_migrations")
  if [[ $actual_schema != "$expected_schema" ]]; then
    echo "Actual $database schema does not match the release manifest; no automatic image rollback or manifest activation was attempted." >&2
    exit 1
  fi
done
if ! python3 - "$scratch/compose.json" <<'PY'
import json,subprocess,sys
c=json.load(open(sys.argv[1]))
for name,service in c["services"].items():
    result=subprocess.run(["docker","ps","--filter",f"label=com.docker.compose.project=teamie","--filter",f"label=com.docker.compose.service={name}","--format","{{.ID}}"],check=True,capture_output=True,text=True)
    ids=result.stdout.split()
    assert len(ids)==1, f"expected one running {name} container"
    actual=subprocess.run(["docker","inspect","--format","{{.Config.Image}}",ids[0]],check=True,capture_output=True,text=True).stdout.strip()
    assert actual==service["image"], f"running {name} image differs from release digest"
PY
then restore_previous; exit 1; fi
for service in identity chat realtime web; do
  case "$service" in
    identity) version_url=http://127.0.0.1:8081/version;;
    chat) version_url=http://127.0.0.1:8082/version;;
    realtime) version_url=http://127.0.0.1:8083/version;;
    web) version_url=http://127.0.0.1/version.json;;
  esac
  if ! "${COMPOSE[@]}" exec -T "$service" wget -q -O - "$version_url" | \
    python3 -c 'import json,sys; actual=json.load(sys.stdin); expected=json.load(open(sys.argv[1])); assert actual["sha"]==expected["source"]["sha"] and actual["version"]==expected["version"], "running version mismatch"' "$manifest"; then
    restore_previous; exit 1
  fi
done
# This verifies the public certificate chain and HTTP routing; never use --insecure.
if ! curl --fail --silent --show-error --retry 20 --retry-all-errors --retry-delay 3 \
  --max-time 15 "$origin/" -o /dev/null; then restore_previous; exit 1; fi
if ! auth_status=$(curl --silent --show-error --max-time 15 --output /dev/null --write-out '%{http_code}' "$origin/api/v1/auth/session"); then
  restore_previous; exit 1
fi
if [[ $auth_status != 401 ]]; then echo 'Public auth route did not return the expected unauthenticated response.' >&2; restore_previous; exit 1; fi

deployment_id=$(python3 -c 'import json,sys; m=json.load(open(sys.argv[1])); print(m["version"]+"-"+m["source"]["sha"])' "$manifest")
record=$(mktemp -d "$TEAMIE_ROOT/state/${deployment_id}-XXXXXX")
cp "$manifest" "$record/release-manifest.json"
cp "$scratch/images.env" "$record/images.env"
ln -s "$recipe" "$record/recipe"
ln -s "$recipe/ops" "$record/ops"
python3 - "$scratch/compose.json" "$manifest" "$mode" > "$record/deployment.json" <<'PY'
import datetime,hashlib,json,sys
c=json.load(open(sys.argv[1])); m=json.load(open(sys.argv[2]))
path=next(v["source"] for v in c["services"]["identity"]["volumes"] if v["target"]=="/etc/teamie/feature-flags.json")
flags=open(path,"rb").read()
print(json.dumps({"deployedAt":datetime.datetime.now(datetime.timezone.utc).isoformat(),"operation":sys.argv[3],"version":m["version"],"sha":m["source"]["sha"],"runtimeFeatureFlagsSha256":hashlib.sha256(flags).hexdigest(),"runtimeFeatureFlagsRevision":json.loads(flags)["revision"]},indent=2))
PY
mkdir -p "$TEAMIE_ROOT/state/history"
if [[ -e $current ]]; then
  ln -sfn "$(readlink "$current")" "$TEAMIE_ROOT/previous.next"
  mv -Tf "$TEAMIE_ROOT/previous.next" "$TEAMIE_ROOT/previous"
fi
ln -sfn "$record" "$TEAMIE_ROOT/current.next"
mv -Tf "$TEAMIE_ROOT/current.next" "$current"
if [[ $mode == apply ]]; then
  cp "$manifest" "$TEAMIE_ROOT/state/highwater.next"
  mv -f "$TEAMIE_ROOT/state/highwater.next" "$TEAMIE_ROOT/state/highwater.json"
  if [[ ! -e $TEAMIE_ROOT/state/history/$deployment_id.json ]]; then
    cp "$manifest" "$TEAMIE_ROOT/state/history/$deployment_id.json"
  fi
  version=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$manifest")
  if [[ ! -e $TEAMIE_ROOT/state/history/$version.json ]]; then
    cp "$manifest" "$TEAMIE_ROOT/state/history/$version.json"
  fi
fi
echo "Production $mode succeeded: $deployment_id"
