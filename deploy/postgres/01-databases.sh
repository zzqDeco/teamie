#!/usr/bin/env bash
# Used both by the empty-volume entrypoint and the explicit forward upgrade gate.
# Never alter existing role passwords or application data. Verify each credential.
set -euo pipefail
export PGPASSWORD="$POSTGRES_PASSWORD"
for suffix in identity chat agent_control agent_runtime gateway; do
  database="teamie_$suffix"
  upper_suffix=$(printf '%s' "$suffix" | tr '[:lower:]' '[:upper:]')
  variable="POSTGRES_${upper_suffix}_PASSWORD"
  export TEAMIE_DB_PASSWORD=${!variable:?database password required}
  psql --username postgres --dbname postgres --set ON_ERROR_STOP=1 --set database="$database" <<'SQL'
\getenv db_password TEAMIE_DB_PASSWORD
SELECT format('CREATE ROLE %I LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE', :'database', :'db_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname=:'database') \gexec
SELECT format('CREATE DATABASE %I OWNER %I', :'database', :'database')
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname=:'database') \gexec
SELECT format('REVOKE ALL ON DATABASE %I FROM PUBLIC', :'database') \gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'database', :'database') \gexec
SELECT 1 / CASE WHEN (SELECT NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls FROM pg_roles WHERE rolname=:'database')
 AND (SELECT pg_get_userbyid(datdba)=:'database' FROM pg_database WHERE datname=:'database') THEN 1 ELSE 0 END;
SQL
  if [[ ${TEAMIE_VERIFY_DB_CREDENTIALS:-0} == 1 ]]; then
    PGPASSWORD="$TEAMIE_DB_PASSWORD" psql --host 127.0.0.1 --username "$database" --dbname "$database" \
    --set ON_ERROR_STOP=1 --command 'SELECT 1' >/dev/null
  fi
  unset TEAMIE_DB_PASSWORD
done
