#!/usr/bin/env bash
# GitHub hosted-runner client. Secrets are passed only through environment/stdin.
set -euo pipefail
umask 077
mode=${1:-apply}
[[ $mode == apply || $mode == rollback ]] || exit 1
: "${PRODUCTION_SSH_HOST:?}" "${PRODUCTION_SSH_USER:?}" "${PRODUCTION_SSH_KEY:?}" "${PRODUCTION_SSH_KNOWN_HOSTS:?}" "${GHCR_TOKEN:?}" "${GHCR_USER:?}"
[[ $PRODUCTION_SSH_HOST =~ ^[A-Za-z0-9.-]+$ && $PRODUCTION_SSH_USER =~ ^[a-z][a-z0-9-]*$ && $GHCR_USER =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] || exit 1
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
  command="umask 022 && mkdir '/srv/teamie/releases/$name' && tar -xzf '/srv/teamie/incoming/$name.tgz' --no-same-owner --no-same-permissions -C '/srv/teamie/releases/$name' && rm '/srv/teamie/incoming/$name.tgz' && bash '/srv/teamie/releases/$name/ops/deploy.sh' apply '/srv/teamie/releases/$name' '$GHCR_USER'"
else
  : "${ROLLBACK_VERSION:?}" "${EXPECTED_CURRENT_SHA:?}"
  [[ $ROLLBACK_VERSION =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ && $EXPECTED_CURRENT_SHA =~ ^[0-9a-f]{40}$ ]] || exit 1
  command="bash /srv/teamie/current/recipe/ops/deploy.sh rollback '$ROLLBACK_VERSION' '$EXPECTED_CURRENT_SHA' '$GHCR_USER'"
fi
# The remote command intentionally contains only validated, client-side arguments.
# shellcheck disable=SC2029
printf '%s\n' "$GHCR_TOKEN" | ssh "${ssh_opts[@]}" "$remote" "$command"
