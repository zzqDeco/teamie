#!/usr/bin/env bash
# Real dump/restore on the disposable CI PostgreSQL service, never production.
set -euo pipefail
: "${POSTGRES_CONTAINER_ID:?disposable CI PostgreSQL container ID required}"
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
pg=(docker exec -i "$POSTGRES_CONTAINER_ID")
"${pg[@]}" env TEAMIE_VERIFY_DB_CREDENTIALS=1 POSTGRES_IDENTITY_PASSWORD=ci-identity-disposable-password \
  POSTGRES_CHAT_PASSWORD=ci-chat-disposable-password POSTGRES_AGENT_CONTROL_PASSWORD=ci-control-disposable-password \
  POSTGRES_AGENT_RUNTIME_PASSWORD=ci-runtime-disposable-password POSTGRES_GATEWAY_PASSWORD=ci-gateway-disposable-password \
  bash -s < deploy/postgres/01-databases.sh
for suffix in identity chat agent_control agent_runtime gateway; do
  version=1
  if [[ $suffix == identity || $suffix == chat || $suffix == agent_runtime || $suffix == gateway ]]; then version=2; fi
  # Use a known fixture table so the restore asserts data, schema and binary values.
  "${pg[@]}" psql -U postgres -d "teamie_$suffix" -v ON_ERROR_STOP=1 -v ledger="${suffix}_schema_migrations" -v version="$version" <<'SQL'
CREATE TABLE :"ledger"(version INTEGER PRIMARY KEY);
INSERT INTO :"ledger" VALUES(:version);
CREATE TABLE restore_fixture(id INTEGER PRIMARY KEY,payload JSONB NOT NULL,encrypted_value BYTEA NOT NULL);
INSERT INTO restore_fixture VALUES(1,'{"owner":"ci","message":"persisted","count":3}',decode('010203aaff','hex'));
SQL
  "${pg[@]}" pg_dump -U postgres -d "teamie_$suffix" --format custom > "$scratch/$suffix.dump"
  "${pg[@]}" psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE restore_$suffix;"
  "${pg[@]}" pg_restore -U postgres -d "restore_$suffix" --exit-on-error --no-owner --no-acl < "$scratch/$suffix.dump"
  actual=$("${pg[@]}" psql -U postgres -d "restore_$suffix" -At -v ON_ERROR_STOP=1 -c \
    "SELECT (SELECT max(version) FROM ${suffix}_schema_migrations)||':'||(payload->>'message')||':'||encode(encrypted_value,'hex') FROM restore_fixture WHERE id=1;")
  [[ $actual == "$version:persisted:010203aaff" ]] || { echo "Restore verification failed for $suffix" >&2; exit 1; }
  # Ordinary owners may not connect to another service's database.
  if "${pg[@]}" env PGPASSWORD=ci-identity-disposable-password psql -h 127.0.0.1 -U teamie_identity -d teamie_gateway -c 'SELECT 1' >/dev/null 2>&1; then
    echo 'Cross-service database isolation failed.' >&2; exit 1
  fi
  "${pg[@]}" psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE restore_$suffix;"
done
echo 'All five disposable database dumps were restored and their rows, schema ledgers and binary data verified.'
