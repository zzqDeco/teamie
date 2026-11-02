#!/usr/bin/env bash
# Read-only freshness and SHA-256 integrity probe for local Teamie backups.
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
exec "$script_dir/sync-backup-offhost.sh" --check-only "$@"
