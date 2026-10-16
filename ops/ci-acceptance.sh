#!/usr/bin/env bash
# Disposable hosted-runner processes, not an additional deployment environment.
set -euo pipefail
scratch=$(mktemp -d)
pids=()
cleanup() {
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  rm -rf "$scratch"
}
trap cleanup EXIT
export APP_ORIGIN=http://127.0.0.1:8080 COOKIE_SECURE=false
export REDIS_ADDR=127.0.0.1:6379
export REGISTRATION_CODE=ci-disposable-registration-code-only
export FEATURE_FLAGS_FILE="$PWD/deploy/feature-flags.default.json"
for database in teamie_identity teamie_chat; do
  PGPASSWORD=ci-only-postgres psql --host 127.0.0.1 --username postgres --dbname postgres \
    --set ON_ERROR_STOP=1 --command "CREATE DATABASE $database;"
done
for service in identity chat realtime; do
  (cd backend && go build -o "$scratch/$service" "./services/$service/cmd")
done
HTTP_ADDR=:8081 RPC_ADDR=:8881 DATABASE_URL='postgres://postgres:ci-only-postgres@127.0.0.1:5432/teamie_identity?sslmode=disable' \
  "$scratch/identity" > "$scratch/identity.log" 2>&1 & pids+=("$!")
HTTP_ADDR=:8082 RPC_ADDR=:8882 IDENTITY_RPC_ADDR=127.0.0.1:8881 DATABASE_URL='postgres://postgres:ci-only-postgres@127.0.0.1:5432/teamie_chat?sslmode=disable' \
  "$scratch/chat" > "$scratch/chat.log" 2>&1 & pids+=("$!")
HTTP_ADDR=:8083 IDENTITY_RPC_ADDR=127.0.0.1:8881 CHAT_RPC_ADDR=127.0.0.1:8882 \
  "$scratch/realtime" > "$scratch/realtime.log" 2>&1 & pids+=("$!")
for port in 8081 8082 8083; do
  ready=0
  for _ in {1..60}; do
    if curl --fail --silent "http://127.0.0.1:$port/readyz" -o /dev/null; then ready=1; break; fi
    sleep 1
  done
  if [[ $ready != 1 ]]; then
    cat "$scratch/identity.log" "$scratch/chat.log" "$scratch/realtime.log"
    echo "Ephemeral service $port did not become ready." >&2; exit 1
  fi
done
TEAMIE_ACCEPTANCE_URL="$APP_ORIGIN" \
TEAMIE_IDENTITY_URL=http://127.0.0.1:8081 \
TEAMIE_CHAT_URL=http://127.0.0.1:8082 \
TEAMIE_WS_URL=ws://127.0.0.1:8083/api/v1/ws \
  python3 tools/acceptance/im_acceptance.py
