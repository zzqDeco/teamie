#!/usr/bin/env bash
set -euo pipefail
umask 077
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
for service in identity chat realtime web caddy postgres redis; do
  key=$(printf '%s' "$service" | tr '[:lower:]' '[:upper:]')
  printf '%s_IMAGE=ghcr.io/zzqdeco/teamie-%s@sha256:%064d\n' "$key" "$service" 0 >> "$scratch/images.env"
done
docker compose --file deploy/compose.production.yaml --env-file deploy/production.env.example \
  --env-file "$scratch/images.env" config --format json > "$scratch/compose.json"
python3 - "$scratch/compose.json" <<'PY'
import json,sys
config=json.load(open(sys.argv[1]))
for name,service in config['services'].items():
    assert name=='caddy' or not service.get('ports'), 'only the edge may publish ports'
    for mount in service.get('tmpfs',[]):
        assert mount.startswith('/'), 'tmpfs entries must preserve their quoted comma-separated options'
    assert int(service['mem_limit'])>0
assert sum(int(s['mem_limit']) for s in config['services'].values())==960*1024*1024
assert config['networks']['private']['internal'] is True
PY
