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
  if [[ $suffix == identity || $suffix == chat || $suffix == agent_runtime ]]; then version=3; fi
  # Identity's real schema creates its complete production migration ledger.
  if [[ $suffix != identity && $suffix != agent_runtime ]]; then
    "${pg[@]}" psql -U postgres -d "teamie_$suffix" -v ON_ERROR_STOP=1 -v ledger="${suffix}_schema_migrations" -v version="$version" <<'SQL'
CREATE TABLE :"ledger"(version INTEGER PRIMARY KEY);
INSERT INTO :"ledger" VALUES(:version);
SQL
  fi
  # Use a known fixture table so the restore asserts data and binary values.
  "${pg[@]}" psql -U postgres -d "teamie_$suffix" -v ON_ERROR_STOP=1 <<'SQL'
CREATE TABLE restore_fixture(id INTEGER PRIMARY KEY,payload JSONB NOT NULL,encrypted_value BYTEA NOT NULL);
INSERT INTO restore_fixture VALUES(1,'{"owner":"ci","message":"persisted","count":3}',decode('010203aaff','hex'));
SQL
  if [[ $suffix == identity ]]; then
    cat backend/internal/identity/schema.sql backend/internal/identity/migrations/002_principals.sql \
      backend/internal/identity/migrations/003_password_resets.sql | "${pg[@]}" psql -U postgres -d teamie_identity -v ON_ERROR_STOP=1 >/dev/null
    "${pg[@]}" psql -U postgres -d teamie_identity -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO identity_schema_migrations(version) VALUES(2),(3) ON CONFLICT DO NOTHING;
INSERT INTO identity_users(id,email,display_name,kind,password_hash,platform_role)
VALUES('restore-owner','restore@example.test','Restore owner','human','fixture-password-hash','admin');
INSERT INTO identity_sessions(token_hash,user_id,csrf_token,expires_at)
VALUES('fixture-session-hash','restore-owner','fixture-csrf',now()+interval '1 hour');
INSERT INTO registration_invites(id,code_hash,created_by,expires_at)
VALUES('restore-invite','fixture-invite-hash','restore-owner',now()+interval '1 day');
INSERT INTO identity_password_resets(id,user_id,token_hash,created_by,expires_at,used_at,revoked_at)
VALUES('restore-active','restore-owner',repeat('1',64),'restore-owner',now()+interval '1 hour',NULL,NULL),
      ('restore-used','restore-owner',repeat('2',64),'restore-owner',now()+interval '1 hour',now(),NULL),
      ('restore-revoked','restore-owner',repeat('3',64),NULL,now()+interval '1 hour',NULL,now());
SQL
  fi
  if [[ $suffix == chat ]]; then
    # Restore an actual linked binary attachment through Chat's real schema,
    # including bytes that cannot survive a UTF-8/text-file transport.
    cat backend/internal/chat/schema.sql backend/internal/chat/migrations/002_membership_management.sql \
      backend/internal/chat/migrations/003_attachments.sql | "${pg[@]}" psql -U postgres -d teamie_chat -v ON_ERROR_STOP=1 >/dev/null
    "${pg[@]}" psql -U postgres -d teamie_chat -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO workspaces(id,name,created_by) VALUES('restore-workspace','Restore','owner');
INSERT INTO conversations(id,workspace_id,type,name) VALUES('restore-conversation','restore-workspace','channel','general');
INSERT INTO messages(id,conversation_id,sender_id,sender_name,sender_kind,client_message_id,seq,body,original_body_hash,attachments,original_attachment_ids)
VALUES('restore-message','restore-conversation','owner','Owner','human','restore-send',1,'','fixture',
'[{"id":"restore-attachment","filename":"binary.restore.bin","mediaType":"application/octet-stream","sizeBytes":7,"sha256":"fixture"}]','{restore-attachment}');
INSERT INTO attachments(id,conversation_id,uploader_id,message_id,filename,media_type,size_bytes,sha256,content)
VALUES('restore-attachment','restore-conversation','owner','restore-message','binary.restore.bin','application/octet-stream',7,'fixture',decode('00fffe80890a0d','hex'));
SQL
  fi
  if [[ $suffix == agent_runtime ]]; then
    # Exercise the actual embedded forward migrations, not a synthetic ledger.
    python3 - <<'PY' | "${pg[@]}" psql -U postgres -d teamie_agent_runtime -v ON_ERROR_STOP=1 >/dev/null
from pathlib import Path
import re
root=Path('agent-runtime/internal/engine')
sources='\n'.join(p.read_text() for p in root.glob('*.go') if not p.name.endswith('_test.go'))
for name in ('schemaSQL','schemaV2SQL','schemaV3SQL','contextArchiveSchemaSQL','contextRecoverySchemaSQL'):
    match=re.search(r'const\s+'+name+r'\s*=\s*`([^`]+)`',sources,re.S)
    if match is None:
        raise SystemExit('missing actual runtime migration: '+name)
    print(match[1])
PY
    runtime_context_budget=$(cat ops/fixtures/runtime-context-budget-v1.json)
    "${pg[@]}" psql -U postgres -d teamie_agent_runtime -v ON_ERROR_STOP=1 -v frozen_budget="$runtime_context_budget" <<'SQL'
INSERT INTO runtime_agents(agent_id,cursor) VALUES('10000000-0000-4000-8000-000000000001',42);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','restore-context','[]',
       '30000000-0000-4000-8000-000000000001','teamie-context-v038',:'frozen_budget'::jsonb,0,0);
INSERT INTO runtime_context_artifacts(agent_id,run_id,attempt_id,artifact_key,kind,content,content_hash,sources,source_event_seq)
VALUES('10000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001',
       'transcript:restore','transcript',decode('00fffe80890a0d','hex'),repeat('a',64),
       '[{"eventId":"42","conversationId":"40000000-0000-4000-8000-000000000001","messageId":"50000000-0000-4000-8000-000000000001"}]',42);
INSERT INTO runtime_context_recoveries(run_id,model_step,context_profile,request_hash,streaming,error_code,decision_version,decision_json)
VALUES('20000000-0000-4000-8000-000000000001',2,'teamie-context-v038',repeat('b',64),true,'context_overflow',1,jsonb_build_object('version',1,'action','compact_prefix','budget',:'frozen_budget'::jsonb));
SQL
  fi
  "${pg[@]}" pg_dump -U postgres -d "teamie_$suffix" --format custom > "$scratch/$suffix.dump"
  "${pg[@]}" psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE restore_$suffix;"
  "${pg[@]}" pg_restore -U postgres -d "restore_$suffix" --exit-on-error --no-owner --no-acl < "$scratch/$suffix.dump"
  actual=$("${pg[@]}" psql -U postgres -d "restore_$suffix" -At -v ON_ERROR_STOP=1 -c \
    "SELECT (SELECT max(version) FROM ${suffix}_schema_migrations)||':'||(payload->>'message')||':'||encode(encrypted_value,'hex') FROM restore_fixture WHERE id=1;")
  [[ $actual == "$version:persisted:010203aaff" ]] || { echo "Restore verification failed for $suffix" >&2; exit 1; }
  if [[ $suffix == identity ]]; then
    actual=$("${pg[@]}" psql -U postgres -d restore_identity -At -v ON_ERROR_STOP=1 -c \
      "SELECT (SELECT string_agg(version::text,',' ORDER BY version) FROM identity_schema_migrations)||':'||(SELECT count(applied_at) FROM identity_schema_migrations)||':'||u.platform_role||':'||u.password_hash||':'||(SELECT count(*) FROM identity_sessions WHERE user_id=u.id)||':'||(SELECT count(*) FROM registration_invites WHERE created_by=u.id)||':'||(SELECT count(*) FROM identity_password_resets WHERE user_id=u.id AND used_at IS NULL AND revoked_at IS NULL AND token_hash=repeat('1',64))||':'||(SELECT count(*) FROM identity_password_resets WHERE user_id=u.id AND used_at IS NOT NULL AND revoked_at IS NULL AND token_hash=repeat('2',64))||':'||(SELECT count(*) FROM identity_password_resets WHERE user_id=u.id AND used_at IS NULL AND revoked_at IS NOT NULL AND token_hash=repeat('3',64) AND created_by IS NULL) FROM identity_users u WHERE u.id='restore-owner';")
    [[ $actual == '1,2,3:3:admin:fixture-password-hash:1:1:1:1:1' ]] || { echo 'Identity account, session, invitation and password reset restore verification failed.' >&2; exit 1; }
  fi
  if [[ $suffix == chat ]]; then
    actual=$("${pg[@]}" psql -U postgres -d restore_chat -At -v ON_ERROR_STOP=1 -c \
      "SELECT encode(a.content,'hex')||':'||a.filename||':'||(m.attachments->0->>'id')||':'||m.original_attachment_ids[1] FROM attachments a JOIN messages m ON m.id=a.message_id WHERE a.id='restore-attachment' AND a.size_bytes=7 AND m.body='';")
    [[ $actual == '00fffe80890a0d:binary.restore.bin:restore-attachment:restore-attachment' ]] || { echo 'Chat attachment binary restore verification failed.' >&2; exit 1; }
  fi
  if [[ $suffix == agent_runtime ]]; then
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v frozen_budget="$runtime_context_budget" <<'SQL'
SELECT (SELECT string_agg(version::text,',' ORDER BY version) FROM agent_runtime_schema_migrations)||':'||r.context_profile||':'||(r.context_budget=:'frozen_budget'::jsonb)::text||':'||encode(a.content,'hex')||':'||(a.sources->0->>'eventId')||':'||a.source_event_seq||':'||c.model_step||':'||c.error_code||':'||c.streaming::text||':'||(c.decision_json=jsonb_build_object('version',1,'action','compact_prefix','budget',:'frozen_budget'::jsonb))::text FROM runtime_runs r JOIN runtime_context_artifacts a ON a.run_id=r.id JOIN runtime_context_recoveries c ON c.run_id=r.id WHERE r.id='20000000-0000-4000-8000-000000000001';
SQL
)
    [[ $actual == '1,2,3:teamie-context-v038:true:00fffe80890a0d:42:42:2:context_overflow:true:true' ]] || { echo 'Runtime frozen policy, context archive, source references and recovery tape restore failed.' >&2; exit 1; }
  fi
  # Ordinary owners may not connect to another service's database.
  if "${pg[@]}" env PGPASSWORD=ci-identity-disposable-password psql -h 127.0.0.1 -U teamie_identity -d teamie_gateway -c 'SELECT 1' >/dev/null 2>&1; then
    echo 'Cross-service database isolation failed.' >&2; exit 1
  fi
  "${pg[@]}" psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE restore_$suffix;"
done
echo 'All five disposable database dumps were restored and their rows, schema ledgers and binary data verified.'
