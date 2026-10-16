#!/usr/bin/env bash
# Official postgres entrypoint runs this only for an empty data volume.
set -euo pipefail
psql --username "$POSTGRES_USER" --dbname postgres --set ON_ERROR_STOP=1 \
  --set identity_password="$POSTGRES_IDENTITY_PASSWORD" \
  --set chat_password="$POSTGRES_CHAT_PASSWORD" <<'SQL'
CREATE ROLE teamie_identity LOGIN PASSWORD :'identity_password' NOSUPERUSER NOCREATEDB NOCREATEROLE;
CREATE ROLE teamie_chat LOGIN PASSWORD :'chat_password' NOSUPERUSER NOCREATEDB NOCREATEROLE;
CREATE DATABASE teamie_identity OWNER teamie_identity;
CREATE DATABASE teamie_chat OWNER teamie_chat;
REVOKE ALL ON DATABASE teamie_identity FROM PUBLIC;
REVOKE ALL ON DATABASE teamie_chat FROM PUBLIC;
GRANT CONNECT ON DATABASE teamie_identity TO teamie_identity;
GRANT CONNECT ON DATABASE teamie_chat TO teamie_chat;
SQL
