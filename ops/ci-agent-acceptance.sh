#!/usr/bin/env bash
# Fresh disposable local processes; never runs against production accounts/data.
set -euo pipefail
scratch=$(mktemp -d)
pids=()
databases=()
pg_port=${TEAMIE_CI_PG_PORT:-5432}
pg_user=${TEAMIE_CI_PG_USER:-postgres}
pg_password=${TEAMIE_CI_PG_PASSWORD:-ci-only-postgres}
http_base=${TEAMIE_CI_HTTP_BASE:-8080}
rpc_base=${TEAMIE_CI_RPC_BASE:-8880}
[[ $pg_port =~ ^[0-9]+$ && $http_base =~ ^[0-9]+$ && $rpc_base =~ ^[0-9]+$ && $pg_user =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]] || exit 1
pg=(psql --host 127.0.0.1 --port "$pg_port" --username "$pg_user" --dbname postgres --set ON_ERROR_STOP=1)
cleanup() {
  for pid in "${pids[@]-}"; do [[ -n $pid ]] || continue; kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]-}"; do [[ -n $pid ]] || continue; wait "$pid" 2>/dev/null || true; done
  for database in "${databases[@]-}"; do [[ -n $database ]] || continue; PGPASSWORD="$pg_password" "${pg[@]}" --command "DROP DATABASE $database;" >/dev/null || true; done
  rm -rf "$scratch"
}
trap cleanup EXIT
export APP_ORIGIN="http://127.0.0.1:$http_base" COOKIE_SECURE=false REDIS_ADDR=${TEAMIE_CI_REDIS_ADDR:-127.0.0.1:6379}
export BOOTSTRAP_INVITE_CODE=ci-agent-bootstrap-disposable-only
export FEATURE_FLAGS_FILE="$PWD/deploy/feature-flags.default.json"
export IM_PRINCIPAL_CONTROL_TOKEN=ci-only-principal-control-credential IM_PRINCIPAL_RUNTIME_TOKEN=ci-only-principal-runtime-credential
export AGENT_RUNTIME_TOKEN=ci-only-agent-control-runtime-credential GATEWAY_RUNTIME_TOKEN=ci-only-model-gateway-runtime-credential
export SANDBOX_RUNTIME_TOKEN=ci-only-sandbox-runtime-credential
export GATEWAY_MASTER_KEY=abababababababababababababababababababababababababababababababab
export IDENTITY_RPC_ADDR="127.0.0.1:$((rpc_base+1))" CHAT_RPC_ADDR="127.0.0.1:$((rpc_base+2))"
export AGENT_CONTROL_RPC_ADDR="127.0.0.1:$((rpc_base+4))" GATEWAY_RPC_ADDR="127.0.0.1:$((rpc_base+6))" SANDBOX_RPC_ADDR="127.0.0.1:$((rpc_base+7))"
export GATEWAY_STREAM_URL="http://127.0.0.1:$((http_base+6))/internal/v1/chat/stream"
export IM_API_URL="http://127.0.0.1:$((http_base+2))"
if [[ ${TEAMIE_AGENT_SKIP_SANDBOX:-0} != 1 ]]; then
  : "${SANDBOX_TEST_RUNNER_IMAGE:?disposable CI runner image ID required}"
  export SANDBOX_RUNNER_IMAGE=$SANDBOX_TEST_RUNNER_IMAGE
fi
export SANDBOX_WORKSPACE_ROOT="$scratch/workspaces" SANDBOX_STATE_ROOT="$scratch/receipts"
for suffix in identity chat agent_control agent_runtime gateway; do
  database="agent_accept_$$_$suffix"
  PGPASSWORD="$pg_password" "${pg[@]}" --command "CREATE DATABASE $database;" >/dev/null
  databases+=("$database")
done
for service in identity chat realtime; do
  (cd backend && go build -o "$scratch/$service" "./services/$service/cmd")
done
(cd agent-control && go build -o "$scratch/agent-control" ./cmd)
(cd agent-runtime && go build -o "$scratch/agent-runtime" ./cmd)
for service in llm-gateway sandbox; do
  (cd agent-infra && go build -o "$scratch/$service" "./cmd/$service")
done
launch() {
  local service=$1 offset=$2 suffix=$3
  shift 3
  HTTP_ADDR=":$((http_base+offset))" DATABASE_URL="postgres://$pg_user:$pg_password@127.0.0.1:$pg_port/agent_accept_$$_$suffix?sslmode=disable" \
    "$@" "$scratch/$service" > "$scratch/$service.log" 2>&1 & pids+=("$!")
}
launch identity 1 identity env RPC_ADDR=":$((rpc_base+1))"
launch chat 2 chat env RPC_ADDR=":$((rpc_base+2))"
launch realtime 3 unused env
launch agent-control 4 agent_control env RPC_ADDR=":$((rpc_base+4))"
launch llm-gateway 6 gateway env RPC_ADDR=":$((rpc_base+6))"
offsets=(1 2 3 4 5 6)
if [[ ${TEAMIE_AGENT_SKIP_SANDBOX:-0} == 1 ]]; then
  echo 'SKIP: local diagnostic has no Docker supervisor; execution isolation is verified separately in required hosted CI.'
else
  launch sandbox 7 unused env RPC_ADDR=":$((rpc_base+7))"
  offsets+=(7)
fi
launch agent-runtime 5 agent_runtime env RPC_ADDR=":$((rpc_base+5))"
for offset in "${offsets[@]}"; do
  port=$((http_base+offset))
  ready=0
  for _ in {1..90}; do
    if curl --fail --silent "http://127.0.0.1:$port/readyz" -o /dev/null; then ready=1; break; fi
    sleep 1
  done
  if [[ $ready != 1 ]]; then
    # Only disposable fixture logs may be printed; production scripts never do this.
    cat "$scratch/"*.log
    echo "Disposable Agent service $port did not become ready." >&2; exit 1
  fi
done
TEAMIE_AGENT_ACCEPTANCE_DISPOSABLE=1 \
TEAMIE_ACCEPTANCE_URL="$APP_ORIGIN" TEAMIE_IDENTITY_URL="http://127.0.0.1:$((http_base+1))" \
TEAMIE_CHAT_URL="http://127.0.0.1:$((http_base+2))" TEAMIE_CONTROL_URL="http://127.0.0.1:$((http_base+4))" \
TEAMIE_GATEWAY_URL="http://127.0.0.1:$((http_base+6))" python3 ops/acceptance-agents.py
