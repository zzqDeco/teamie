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
  # Compare the real restored migration ledger with the generated release
  # contract, so updating a migration without its manifest fails before deploy.
  version=$(python3 -c 'import sys; from ops.manifest import SCHEMA_V2; print(SCHEMA_V2[sys.argv[1]])' "$suffix")
  # Identity's real schema creates its complete production migration ledger.
  if [[ $suffix != identity && $suffix != agent_runtime && $suffix != chat ]]; then
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
      backend/internal/chat/migrations/003_attachments.sql backend/internal/chat/migrations/004_attachment_uploads.sql \
      backend/internal/chat/migrations/005_mentions.sql | "${pg[@]}" psql -U postgres -d teamie_chat -v ON_ERROR_STOP=1 >/dev/null
    "${pg[@]}" psql -U postgres -d teamie_chat -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO workspaces(id,name,created_by) VALUES('restore-workspace','Restore','owner');
INSERT INTO chat_schema_migrations(version) VALUES(2),(3),(4),(5) ON CONFLICT DO NOTHING;
INSERT INTO conversations(id,workspace_id,type,name) VALUES('restore-conversation','restore-workspace','channel','general');
INSERT INTO messages(id,conversation_id,sender_id,sender_name,sender_kind,client_message_id,seq,body,original_body_hash,attachments,original_attachment_ids)
VALUES('restore-message','restore-conversation','owner','Owner','human','restore-send',1,'','fixture',
'[{"id":"restore-attachment","filename":"binary.restore.bin","mediaType":"application/octet-stream","sizeBytes":7,"sha256":"fixture"}]','{restore-attachment}');
INSERT INTO attachments(id,conversation_id,uploader_id,message_id,filename,media_type,size_bytes,sha256,content)
VALUES('restore-attachment','restore-conversation','owner','restore-message','binary.restore.bin','application/octet-stream',7,'fixture',decode('00fffe80890a0d','hex'));
INSERT INTO attachment_uploads(uploader_id,upload_key,conversation_id,fingerprint,attachment_id)
VALUES('owner','restore-upload','restore-conversation','fixture-fingerprint','restore-attachment'),
      ('owner','restore-removed','restore-conversation','removed-fingerprint',NULL);
UPDATE messages SET body='@Owner',mentions='[{"userId":"owner","start":0,"end":6}]',original_mentions='[{"userId":"owner","start":0,"end":6}]' WHERE id='restore-message';
SQL
  fi
  if [[ $suffix == agent_runtime ]]; then
    # Exercise the actual embedded forward migrations, not a synthetic ledger.
    python3 - <<'PY' | "${pg[@]}" psql -U postgres -d teamie_agent_runtime -v ON_ERROR_STOP=1 >/dev/null
from pathlib import Path
import re
root=Path('agent-runtime/internal/engine')
sources='\n'.join(p.read_text() for p in root.glob('*.go') if not p.name.endswith('_test.go'))
print("SET teamie.runtime_writer_schema='10';")
for name in ('schemaSQL','schemaV2SQL','schemaV3SQL','contextArchiveSchemaSQL','contextRecoverySchemaSQL','schemaV4SQL','schemaV5SQL','schemaV6SQL','schemaV7SQL','schemaV8SQL','schemaV9SQL','schemaV10SQL'):
    match=re.search(r'const\s+'+name+r'\s*=\s*`([^`]+)`',sources,re.S)
    if match is None:
        raise SystemExit('missing actual runtime migration: '+name)
    print(match[1])
PY
    runtime_context_budget=$(cat ops/fixtures/runtime-context-budget-v1.json)
    runtime_output_budget=$(cat ops/fixtures/runtime-context-budget-v2.json)
    runtime_current_budget=$(cat ops/fixtures/runtime-context-budget-v3.json)
    runtime_history_budget=$(cat ops/fixtures/runtime-context-budget-v4.json)
    runtime_notification_budget=$(cat ops/fixtures/runtime-context-budget-v5.json)
    runtime_long_budget=$(cat ops/fixtures/runtime-context-budget-v6.json)
    runtime_calibrated_budget=$(cat ops/fixtures/runtime-context-budget-v7.json)
    runtime_event_driven_budget=$(cat ops/fixtures/runtime-context-budget-event-driven-v1.json)
    "${pg[@]}" psql -U postgres -d teamie_agent_runtime -v ON_ERROR_STOP=1 -v frozen_budget="$runtime_context_budget" -v output_budget="$runtime_output_budget" -v current_budget="$runtime_current_budget" -v history_budget="$runtime_history_budget" -v notification_budget="$runtime_notification_budget" -v long_budget="$runtime_long_budget" -v calibrated_budget="$runtime_calibrated_budget" -v event_driven_budget="$runtime_event_driven_budget" <<'SQL'
INSERT INTO runtime_agents(agent_id,cursor) VALUES('10000000-0000-4000-8000-000000000001',42);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','restore-context','[]',
       '30000000-0000-4000-8000-000000000001','teamie-context-v038',:'frozen_budget'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000002','10000000-0000-4000-8000-000000000001','restore-file-policy','[]',
       '30000000-0000-4000-8000-000000000002','teamie-context-v038',:'frozen_budget'::jsonb||'{"fileDelivery":true,"structuredMentions":true}'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000003','10000000-0000-4000-8000-000000000001','restore-output-policy','[]',
       '30000000-0000-4000-8000-000000000003','teamie-context-v038',:'output_budget'::jsonb||'{"fileDelivery":true,"structuredMentions":true}'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000004','10000000-0000-4000-8000-000000000001','restore-current-policy','[]',
       '30000000-0000-4000-8000-000000000004','teamie-context-v038',:'current_budget'::jsonb||'{"fileDelivery":true,"structuredMentions":true}'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000005','10000000-0000-4000-8000-000000000001','restore-history-policy','[]',
       '30000000-0000-4000-8000-000000000005','teamie-context-v038',:'history_budget'::jsonb||'{"fileDelivery":true,"structuredMentions":true}'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000006','10000000-0000-4000-8000-000000000001','restore-notification-policy','[]',
       '30000000-0000-4000-8000-000000000006','teamie-context-v038',:'notification_budget'::jsonb||'{"fileDelivery":true,"structuredMentions":true}'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000007','10000000-0000-4000-8000-000000000001','restore-long-policy','[]',
       '30000000-0000-4000-8000-000000000007','teamie-context-v038',:'long_budget'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000008','10000000-0000-4000-8000-000000000001','restore-event-driven-policy','[]',
       '30000000-0000-4000-8000-000000000008','teamie-context-v038',:'event_driven_budget'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000009','10000000-0000-4000-8000-000000000001','restore-calibrated-policy','[]',
       '30000000-0000-4000-8000-000000000009','teamie-context-v038',:'calibrated_budget'::jsonb,0,0);
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
      "SELECT encode(a.content,'hex')||':'||a.filename||':'||(m.attachments->0->>'id')||':'||m.original_attachment_ids[1] FROM attachments a JOIN messages m ON m.id=a.message_id WHERE a.id='restore-attachment' AND a.size_bytes=7 AND m.body='@Owner' AND m.mentions=m.original_mentions AND m.mentions='[{\"userId\":\"owner\",\"start\":0,\"end\":6}]'::jsonb;")
    [[ $actual == '00fffe80890a0d:binary.restore.bin:restore-attachment:restore-attachment' ]] || { echo 'Chat attachment binary restore verification failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_chat -At -v ON_ERROR_STOP=1 -c \
      "SELECT (SELECT string_agg(version::text,',' ORDER BY version) FROM chat_schema_migrations)||':'||(SELECT attachment_id FROM attachment_uploads WHERE uploader_id='owner' AND upload_key='restore-upload')||':'||(SELECT count(*) FROM attachment_uploads WHERE upload_key='restore-removed' AND attachment_id IS NULL);")
    [[ $actual == '1,2,3,4,5:restore-attachment:1' ]] || { echo 'Chat upload receipts and tombstones restore verification failed.' >&2; exit 1; }
  fi
  if [[ $suffix == agent_runtime ]]; then
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v frozen_budget="$runtime_context_budget" <<'SQL'
SELECT (SELECT string_agg(version::text,',' ORDER BY version) FROM agent_runtime_schema_migrations)||':'||r.context_profile||':'||(r.context_budget=:'frozen_budget'::jsonb)::text||':'||encode(a.content,'hex')||':'||(a.sources->0->>'eventId')||':'||a.source_event_seq||':'||c.model_step||':'||c.error_code||':'||c.streaming::text||':'||(c.decision_json=jsonb_build_object('version',1,'action','compact_prefix','budget',:'frozen_budget'::jsonb))::text FROM runtime_runs r JOIN runtime_context_artifacts a ON a.run_id=r.id JOIN runtime_context_recoveries c ON c.run_id=r.id WHERE r.id='20000000-0000-4000-8000-000000000001';
SQL
)
    [[ $actual == '1,2,3,4,5,6,7,8,9,10:teamie-context-v038:true:00fffe80890a0d:42:42:2:context_overflow:true:true' ]] || { echo 'Runtime frozen policy, context archive, source references and recovery tape restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v frozen_budget="$runtime_context_budget" <<'SQL'
SELECT context_budget=(:'frozen_budget'::jsonb||'{"fileDelivery":true,"structuredMentions":true}'::jsonb) FROM runtime_runs WHERE input_key='restore-file-policy';
SQL
)
    [[ $actual == t ]] || { echo 'Runtime immutable file-tool admission restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v output_budget="$runtime_output_budget" <<'SQL'
SELECT context_budget=(:'output_budget'::jsonb||'{"fileDelivery":true,"structuredMentions":true}'::jsonb) FROM runtime_runs WHERE input_key='restore-output-policy';
SQL
)
    [[ $actual == t ]] || { echo 'Runtime frozen output recovery policy restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v current_budget="$runtime_current_budget" <<'SQL'
SELECT context_budget=(:'current_budget'::jsonb||'{"fileDelivery":true,"structuredMentions":true}'::jsonb) FROM runtime_runs WHERE input_key='restore-current-policy';
SQL
)
    [[ $actual == t ]] || { echo 'Runtime current-input policy restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v history_budget="$runtime_history_budget" -v notification_budget="$runtime_notification_budget" <<'SQL'
SELECT context_budget=(:'history_budget'::jsonb||'{"fileDelivery":true,"structuredMentions":true}'::jsonb) FROM runtime_runs WHERE input_key='restore-history-policy';
SQL
)
    [[ $actual == t ]] || { echo 'Runtime history coalescing policy restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v notification_budget="$runtime_notification_budget" <<'SQL'
SELECT context_budget=(:'notification_budget'::jsonb||'{"fileDelivery":true,"structuredMentions":true}'::jsonb) FROM runtime_runs WHERE input_key='restore-notification-policy';
SQL
)
    [[ $actual == t ]] || { echo 'Runtime task notification policy restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v long_budget="$runtime_long_budget" <<'SQL'
SELECT context_budget=:'long_budget'::jsonb AND context_budget->>'contextWindowTokens'='1000000' AND context_budget->>'version'='6' FROM runtime_runs WHERE input_key='restore-long-policy';
SQL
)
    [[ $actual == t ]] || { echo 'Runtime million-token policy restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v event_driven_budget="$runtime_event_driven_budget" <<'SQL'
SELECT context_budget=:'event_driven_budget'::jsonb AND context_budget->>'version'='6' AND context_budget->>'eventDrivenInputs'='true' FROM runtime_runs WHERE input_key='restore-event-driven-policy';
SQL
)
    [[ $actual == t ]] || { echo 'Runtime event-driven policy restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v calibrated_budget="$runtime_calibrated_budget" <<'SQL'
SELECT context_budget=:'calibrated_budget'::jsonb AND context_budget->>'version'='7' AND context_budget->>'counterVersion'='teamie-visible-bytes-div4-v1' AND context_budget->>'triggerTokens'='781804' FROM runtime_runs WHERE input_key='restore-calibrated-policy';
SQL
)
    [[ $actual == t ]] || { echo 'Runtime calibrated v7 policy restore failed.' >&2; exit 1; }

  fi
  # Ordinary owners may not connect to another service's database.
  if "${pg[@]}" env PGPASSWORD=ci-identity-disposable-password psql -h 127.0.0.1 -U teamie_identity -d teamie_gateway -c 'SELECT 1' >/dev/null 2>&1; then
    echo 'Cross-service database isolation failed.' >&2; exit 1
  fi
  "${pg[@]}" psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE restore_$suffix;"
done
echo 'All five disposable database dumps were restored and their rows, schema ledgers and binary data verified.'
