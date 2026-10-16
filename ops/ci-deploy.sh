#!/usr/bin/env bash
# GitHub hosted-runner client. Secrets are passed only through environment/stdin.
set -euo pipefail
umask 077
mode=${1:-apply}
[[ $mode == apply || $mode == rollback ]] || exit 1
: "${PRODUCTION_SSH_HOST:?}" "${PRODUCTION_SSH_USER:?}" "${PRODUCTION_SSH_KEY:?}" "${PRODUCTION_SSH_KNOWN_HOSTS:?}" "${GITHUB_RUN_ID:?}" "${GITHUB_RUN_ATTEMPT:?}"
[[ $PRODUCTION_SSH_HOST =~ ^[A-Za-z0-9.-]+$ && $PRODUCTION_SSH_USER =~ ^[a-z][a-z0-9-]*$ && $GITHUB_RUN_ID =~ ^[0-9]+$ && $GITHUB_RUN_ATTEMPT =~ ^[0-9]+$ ]] || exit 1
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
printf '%s\n' "$PRODUCTION_SSH_KEY" > "$scratch/key"
printf '%s\n' "$PRODUCTION_SSH_KNOWN_HOSTS" > "$scratch/known_hosts"
unset PRODUCTION_SSH_KEY PRODUCTION_SSH_KNOWN_HOSTS
ssh_opts=(-i "$scratch/key" -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o "UserKnownHostsFile=$scratch/known_hosts" -o ConnectTimeout=15 -o ServerAliveInterval=30 -o ServerAliveCountMax=10)
remote="$PRODUCTION_SSH_USER@$PRODUCTION_SSH_HOST"
if [[ $mode == apply ]]; then
  : "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}" "${GITHUB_RUN_ID:?}" "${GITHUB_RUN_ATTEMPT:?}"
  [[ $RELEASE_VERSION =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ && $RELEASE_SHA =~ ^[0-9a-f]{40}$ && $GITHUB_RUN_ID =~ ^[0-9]+$ && $GITHUB_RUN_ATTEMPT =~ ^[0-9]+$ ]] || exit 1
  name="$RELEASE_VERSION-$RELEASE_SHA-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
  tar -czf "$scratch/bundle.tgz" deploy ops release-manifest.json
  # All interpolated remote arguments above are constrained to safe characters.
  scp "${ssh_opts[@]}" "$scratch/bundle.tgz" "$remote:/srv/teamie/incoming/$name.tgz"
  command="umask 022 && mkdir '/srv/teamie/releases/$name' && tar -xzf '/srv/teamie/incoming/$name.tgz' --no-same-owner --no-same-permissions -C '/srv/teamie/releases/$name' && rm '/srv/teamie/incoming/$name.tgz' && bash '/srv/teamie/releases/$name/ops/deploy.sh' apply '/srv/teamie/releases/$name'"
else
  : "${ROLLBACK_VERSION:?}" "${EXPECTED_CURRENT_SHA:?}"
  [[ $ROLLBACK_VERSION =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ && $EXPECTED_CURRENT_SHA =~ ^[0-9a-f]{40}$ ]] || exit 1
  name="rollback-$ROLLBACK_VERSION-$EXPECTED_CURRENT_SHA-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
  # A historical current recipe may contain the GHCR-only auth protocol. Use
  # reviewed current ops as a separate helper; preserve archives/current recipe.
  tar -czf "$scratch/bundle.tgz" ops
  scp "${ssh_opts[@]}" "$scratch/bundle.tgz" "$remote:/srv/teamie/incoming/$name.tgz"
  command="umask 022 && mkdir '/srv/teamie/releases/$name' && tar -xzf '/srv/teamie/incoming/$name.tgz' --no-same-owner --no-same-permissions -C '/srv/teamie/releases/$name' && rm '/srv/teamie/incoming/$name.tgz' && bash '/srv/teamie/releases/$name/ops/deploy.sh' rollback '$ROLLBACK_VERSION' '$EXPECTED_CURRENT_SHA'"
fi
# The remote command intentionally contains only validated, client-side arguments.
# shellcheck disable=SC2029
python3 - <<'PYAUTH' | ssh "${ssh_opts[@]}" "$remote" "$command"
import json, os, sys
# Do not fall back across providers: ACR credentials must never be sent to GHCR.
json.dump({"acr": {"username": os.environ.get("REGISTRY_USERNAME", ""),
                   "password": os.environ.get("REGISTRY_PASSWORD", "")},
           "ghcr": {"username": os.environ.get("LEGACY_GHCR_USER", ""),
                    "password": os.environ.get("LEGACY_GHCR_TOKEN", "")}}, sys.stdout)
PYAUTH
