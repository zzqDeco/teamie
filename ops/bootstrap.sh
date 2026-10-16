#!/usr/bin/env bash
# Run once as root on the production host. Never receives private keys.
set -euo pipefail
umask 077

[[ ${EUID} -eq 0 ]] || { echo 'Run bootstrap as root.' >&2; exit 1; }
deploy_user=${TEAMIE_DEPLOY_USER:-teamie-deploy}
[[ $deploy_user =~ ^[a-z][a-z0-9-]{0,30}$ ]] || exit 1
# shellcheck disable=SC1091
source /etc/os-release
[[ $ID == debian && $VERSION_CODENAME == trixie ]] || {
  echo 'This bootstrap supports Debian 13 (trixie) only.' >&2; exit 1;
}
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y --no-install-recommends ca-certificates curl python3 openssh-client util-linux
if ! command -v docker >/dev/null || ! docker compose version >/dev/null 2>&1; then
  for package in docker.io docker-compose docker-doc docker-buildx podman-docker containerd runc; do
    if dpkg-query -W -f='${Status}' "$package" 2>/dev/null | grep -q 'install ok installed'; then
      echo "Conflicting package $package is installed; review its workload before removing it." >&2
      exit 1
    fi
  done
  install -d -m 0755 /etc/apt/keyrings
  curl --fail --silent --show-error --location --retry 3 \
    https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
  chmod 0644 /etc/apt/keyrings/docker.asc
  cat > /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/debian
Suites: trixie
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
  apt-get update -qq
  apt-get install -y --no-install-recommends docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi
systemctl enable --now docker
if ! id "$deploy_user" >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash "$deploy_user"
fi
# Docker membership grants host administration; this account is for delivery only.
usermod -aG docker "$deploy_user"
deploy_home=$(getent passwd "$deploy_user" | cut -d: -f6)
install -d -o "$deploy_user" -g "$deploy_user" -m 0700 "$deploy_home/.ssh"
touch "$deploy_home/.ssh/authorized_keys"
chown "$deploy_user:$deploy_user" "$deploy_home/.ssh/authorized_keys"
chmod 0600 "$deploy_home/.ssh/authorized_keys"
install -d -o "$deploy_user" -g "$deploy_user" -m 0700 \
  /srv/teamie /srv/teamie/config /srv/teamie/tls /srv/teamie/tls/certs \
  /srv/teamie/releases /srv/teamie/incoming /srv/teamie/state /srv/teamie/backups
docker version --format '{{.Server.Version}}'
docker compose version --short
echo 'Bootstrap complete. Add the delivery PUBLIC key to authorized_keys; provision protected production.env and TLS/flag files before deploying.'
