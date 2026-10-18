#!/usr/bin/env bash
set -euo pipefail
umask 077
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
for service in identity chat realtime web caddy postgres redis agent-control agent-runtime llm-gateway sandbox sandbox-runner; do
  key=$(printf '%s' "$service" | tr '[:lower:]-' '[:upper:]_')
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
assert sum(int(s['mem_limit']) for s in config['services'].values())==1472*1024*1024
assert int(config['services']['agent-runtime']['mem_limit'])==256*1024*1024
assert int(config['services']['llm-gateway']['mem_limit'])==128*1024*1024
assert config['networks']['private']['internal'] is True
assert set(config['services']['llm-gateway']['networks'])=={'private','egress'}
assert set(config['services']['sandbox']['networks'])=={'private'}
assert config['services']['sandbox']['environment']['SANDBOX_RUNNER_IMAGE'].startswith('ghcr.io/zzqdeco/teamie-sandbox-runner@sha256:')
for name,service in config['services'].items():
    assert name=='sandbox' or not any(v.get('target')=='/var/run/docker.sock' for v in service.get('volumes',[]))
assert config['services']['caddy']['environment']['TLS_SERVER_NAME']=='YOUR_PUBLIC_IP'
assert config['services']['identity']['environment']['BOOTSTRAP_INVITE_CODE']
assert 'REGISTRATION_CODE' not in config['services']['identity']['environment']
PY
# An empty optional host value must select the explicit first-user default.
BOOTSTRAP_INVITE_CODE='' docker compose --file deploy/compose.production.yaml \
  --env-file deploy/production.env.example --env-file "$scratch/images.env" \
  config --format json > "$scratch/default-bootstrap.json"
python3 - "$scratch/default-bootstrap.json" <<'PY'
import json,sys
assert json.load(open(sys.argv[1]))['services']['identity']['environment']['BOOTSTRAP_INVITE_CODE']=='teamie-bootstrap'
PY
# Render a fully valid disposable secret set and exercise production-only guards.
python3 - "$scratch/production.env" <<'PY'
import hashlib,pathlib,re,sys
text=pathlib.Path('deploy/production.env.example').read_text().replace('YOUR_PUBLIC_IP','192.0.2.1')
text=re.sub(r'REPLACE_[A-Z_]+',lambda m:hashlib.sha256(m.group().encode()).hexdigest(),text)
pathlib.Path(sys.argv[1]).write_text(text)
PY
docker compose --file deploy/compose.production.yaml --env-file "$scratch/production.env" \
  --env-file "$scratch/images.env" config --format json > "$scratch/valid.json"
python3 - "$scratch/valid.json" "$scratch/production.env" <<'PY'
import copy,importlib.util,json,sys
spec=importlib.util.spec_from_file_location('manifest','ops/manifest.py');m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
c=json.load(open(sys.argv[1])); assert m.validate_config(c,sys.argv[2])=='https://192.0.2.1'
for mutate in (
    lambda s:s['llm-gateway']['environment'].update(GATEWAY_MASTER_KEY='invalid'),
    lambda s:s['agent-runtime']['environment'].update(GATEWAY_RUNTIME_TOKEN='x'*64),
    lambda s:s['sandbox']['environment'].update(GATEWAY_MASTER_KEY='a'*64),
    lambda s:s['sandbox']['environment'].update(SANDBOX_RUNNER_IMAGE='python:latest'),
    lambda s:s['agent-control']['environment'].update(DATABASE_URL=s['chat']['environment']['DATABASE_URL']),
    lambda s:s['chat'].setdefault('volumes',[]).append({'target':'/var/run/docker.sock'}),
    lambda s:s['llm-gateway'].update(networks={'private':{}}),
):
    changed=copy.deepcopy(c);mutate(changed['services'])
    try:m.validate_config(changed,sys.argv[2])
    except ValueError:pass
    else:raise AssertionError('unsafe Agent production configuration accepted')
PY

# Render the ACR production endpoint through the real Compose interpolation and
# bind every service plus the execution runner to its selected release manifest.
mkdir "$scratch/acr-digests"
python3 - "$scratch/acr-digests" "$scratch/acr-images.env" <<'PY'
import importlib.util,json,pathlib,sys
spec=importlib.util.spec_from_file_location('manifest','ops/manifest.py');m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
prefix='crpi-teamie-vpc.cn-shenzhen.personal.cr.aliyuncs.com/teamie/teamie-'
images={name:prefix+name+'@sha256:'+'0'*64 for name in m.SERVICES}
for name,image in images.items():
    (pathlib.Path(sys.argv[1])/(name+'.json')).write_text(json.dumps({'service':name,'image':image}))
pathlib.Path(sys.argv[2]).write_text(''.join(name.upper().replace('-','_')+'_IMAGE='+image+'\n' for name,image in images.items()))
PY
python3 ops/manifest.py create --repository zzqDeco/teamie --version v9.0.0 \
  --sha 1111111111111111111111111111111111111111 \
  --main-sha 1111111111111111111111111111111111111111 \
  --digests "$scratch/acr-digests" --flags deploy/feature-flags.default.json \
  --registry-public-host crpi-teamie.cn-shenzhen.personal.cr.aliyuncs.com \
  --registry-pull-host crpi-teamie-vpc.cn-shenzhen.personal.cr.aliyuncs.com \
  --registry-namespace teamie > "$scratch/acr-manifest.json"
docker compose --file deploy/compose.production.yaml --env-file "$scratch/production.env" \
  --env-file "$scratch/acr-images.env" config --format json > "$scratch/acr-compose.json"
python3 ops/manifest.py config "$scratch/acr-compose.json" "$scratch/production.env" \
  --manifest "$scratch/acr-manifest.json" >/dev/null
