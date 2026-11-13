#!/usr/bin/env bash
# Real dump/restore on the disposable CI PostgreSQL service, never production.
set -euo pipefail
: "${POSTGRES_CONTAINER_ID:?disposable CI PostgreSQL container ID required}"
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
pg=(docker exec -i "$POSTGRES_CONTAINER_ID" env PGOPTIONS="-c teamie.runtime_writer_schema=16")
"${pg[@]}" env TEAMIE_VERIFY_DB_CREDENTIALS=1 POSTGRES_IDENTITY_PASSWORD=ci-identity-disposable-password \
  POSTGRES_CHAT_PASSWORD=ci-chat-disposable-password POSTGRES_MARKETPLACE_PASSWORD=ci-marketplace-disposable-password \
  POSTGRES_AGENT_CONTROL_PASSWORD=ci-control-disposable-password \
  POSTGRES_AGENT_RUNTIME_PASSWORD=ci-runtime-disposable-password POSTGRES_GATEWAY_PASSWORD=ci-gateway-disposable-password \
  bash -s < deploy/postgres/01-databases.sh
for suffix in identity chat marketplace agent_control agent_runtime gateway; do
  # Compare the real restored migration ledger with the generated release
  # contract, so updating a migration without its manifest fails before deploy.
  version=$(python3 -c 'import sys; from ops.manifest import SCHEMA_V3; print(SCHEMA_V3[sys.argv[1]])' "$suffix")
  # Use a known fixture table so the restore asserts data and binary values.
  "${pg[@]}" psql -U postgres -d "teamie_$suffix" -v ON_ERROR_STOP=1 <<'SQL'
CREATE TABLE restore_fixture(id INTEGER PRIMARY KEY,payload JSONB NOT NULL,encrypted_value BYTEA NOT NULL);
INSERT INTO restore_fixture VALUES(1,'{"owner":"ci","message":"persisted","count":3}',decode('010203aaff','hex'));
SQL
  if [[ $suffix == identity ]]; then
    cat services/identity/internal/infra/pg/migrations/001_identity.sql services/identity/internal/infra/pg/migrations/002_principals.sql \
      services/identity/internal/infra/pg/migrations/003_password_resets.sql | "${pg[@]}" psql -U postgres -d teamie_identity -v ON_ERROR_STOP=1 >/dev/null
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
    # including bytes that cannot survive a UTF-8/text-file transport, and
    # verify embedded-to-standalone Marketplace data transfer before 010 retires tables.
    cat services/chat/internal/infra/pg/migrations/001_chat.sql services/chat/internal/infra/pg/migrations/002_membership_management.sql \
      services/chat/internal/infra/pg/migrations/003_attachments.sql services/chat/internal/infra/pg/migrations/004_attachment_uploads.sql \
      services/chat/internal/infra/pg/migrations/005_mentions.sql \
      services/chat/internal/infra/pg/migrations/006_conversation_mutations.sql \
      services/chat/internal/infra/pg/migrations/007_reactions_and_notification_levels.sql \
      services/chat/internal/infra/pg/migrations/008_marketplace.sql \
      services/chat/internal/infra/pg/migrations/009_developer_marketplace.sql | "${pg[@]}" psql -U postgres -d teamie_chat -v ON_ERROR_STOP=1 >/dev/null
    "${pg[@]}" psql -U postgres -d teamie_chat -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO workspaces(id,name,created_by) VALUES('restore-workspace','Restore','owner');
INSERT INTO chat_schema_migrations(version) VALUES(2),(3),(4),(5),(6),(7),(8),(9) ON CONFLICT DO NOTHING;
INSERT INTO marketplace_apps(id,slug,name,description,web_url,mcp_url,signing_secret,enabled,owner_id,is_published,published_version,review_status,draft_name,draft_description,draft_web_url,draft_mcp_url,has_draft_changes,submission_revision)
VALUES('legacy-migrated-app','legacy-board','Legacy Board','Migrated from Chat','https://legacy.example.test/ui','https://legacy.example.test/mcp',repeat('m',64),true,'owner',true,2,'approved','Legacy Board','Migrated from Chat','https://legacy.example.test/ui','https://legacy.example.test/mcp',false,2);
INSERT INTO workspace_marketplace_apps(workspace_id,app_id,installed_by,review_preview)
VALUES('restore-workspace','legacy-migrated-app','owner',true);
INSERT INTO marketplace_app_reviews(id,app_id,revision,action,actor_id,note,web_url,mcp_url,snapshot)
VALUES('legacy-migrated-review','legacy-migrated-app',2,'approve','admin','Transferred history','https://legacy.example.test/ui','https://legacy.example.test/mcp','{"migrated":true}'::jsonb);
SQL
    "${pg[@]}" bash -s < deploy/postgres/02-migrate-chat-marketplace.sh
    "${pg[@]}" psql -U postgres -d teamie_chat -v ON_ERROR_STOP=1 < services/chat/internal/infra/pg/migrations/010_retire_embedded_marketplace.sql >/dev/null
    "${pg[@]}" psql -U postgres -d teamie_chat -v ON_ERROR_STOP=1 < services/chat/internal/infra/pg/migrations/011_platform_announcements.sql >/dev/null
    "${pg[@]}" psql -U postgres -d teamie_chat -v ON_ERROR_STOP=1 < services/chat/internal/infra/pg/migrations/012_collaboration.sql >/dev/null
    "${pg[@]}" psql -U postgres -d teamie_chat -v ON_ERROR_STOP=1 < services/chat/internal/infra/pg/migrations/013_group_deletion.sql >/dev/null
    "${pg[@]}" psql -U postgres -d teamie_chat -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO chat_schema_migrations(version) VALUES(10),(11),(12),(13) ON CONFLICT DO NOTHING;
INSERT INTO platform_announcements(id,title,body,author_id,author_name) VALUES('restore-announcement','Teamie v0.4.6','### 更新说明\n- 新增平台公告','owner','Owner');
INSERT INTO platform_announcement_reads(user_id,read_seq) VALUES('owner',1);
INSERT INTO conversations(id,workspace_id,type,name) VALUES('restore-conversation','restore-workspace','channel','general');
INSERT INTO conversation_mutations(actor_id,request_key,request_hash,conversation_id) VALUES('owner','restore-group','fixture-hash','restore-conversation');
INSERT INTO messages(id,conversation_id,sender_id,sender_name,sender_kind,client_message_id,seq,body,original_body_hash,attachments,original_attachment_ids)
VALUES('restore-message','restore-conversation','owner','Owner','human','restore-send',1,'','fixture',
'[{"id":"restore-attachment","filename":"binary.restore.bin","mediaType":"application/octet-stream","sizeBytes":7,"sha256":"fixture"}]','{restore-attachment}');
INSERT INTO attachments(id,conversation_id,uploader_id,message_id,filename,media_type,size_bytes,sha256,content)
VALUES('restore-attachment','restore-conversation','owner','restore-message','binary.restore.bin','application/octet-stream',7,'fixture',decode('00fffe80890a0d','hex'));
INSERT INTO attachment_uploads(uploader_id,upload_key,conversation_id,fingerprint,attachment_id)
VALUES('owner','restore-upload','restore-conversation','fixture-fingerprint','restore-attachment'),
      ('owner','restore-removed','restore-conversation','removed-fingerprint',NULL),
      ('owner','restore-deleted-group-upload','restore-deleted-group','deleted-group-fingerprint',NULL);
UPDATE messages SET body='@Owner',mentions='[{"userId":"owner","start":0,"end":6}]',original_mentions='[{"userId":"owner","start":0,"end":6}]' WHERE id='restore-message';
UPDATE messages SET reactions_version=3 WHERE id='restore-message';
INSERT INTO message_reactions(conversation_id,message_id,emoji,user_id)
VALUES('restore-conversation','restore-message','🟢','owner'),
      ('restore-conversation','restore-message','🟢','reader');
INSERT INTO messages(id,conversation_id,sender_id,sender_name,sender_kind,client_message_id,seq,body,original_body_hash,reactions_version)
VALUES('restore-empty-reactions','restore-conversation','owner','Owner','human','restore-empty-send',2,'Persist the empty reaction watermark','fixture',4);
INSERT INTO conversation_notification_levels(conversation_id,user_id,level)
VALUES('restore-conversation','owner','mentions'),('restore-conversation','reader','none');
INSERT INTO conversation_pins(conversation_id,message_id,pinned_by,pinned_by_name)
VALUES('restore-conversation','restore-message','owner','Owner');
INSERT INTO saved_messages(user_id,conversation_id,message_id)
VALUES('owner','restore-conversation','restore-message');
INSERT INTO read_cursors(conversation_id,user_id,seq,manual_unread_seq)
VALUES('restore-conversation','owner',2,1);
INSERT INTO attachment_thumbnails(attachment_id,media_type,content)
VALUES('restore-attachment','image/png',decode('89504e470d0a1a0a','hex'));
SQL
  fi
  if [[ $suffix == marketplace ]]; then
    "${pg[@]}" psql -U postgres -d teamie_marketplace -v ON_ERROR_STOP=1 <<'SQL'
SET ROLE teamie_marketplace;
INSERT INTO marketplace_apps(id,slug,name,description,web_url,mcp_url,signing_secret,enabled,owner_id,is_published,published_version,review_status,draft_name,draft_description,draft_web_url,draft_mcp_url,has_draft_changes,submission_revision)
VALUES('restore-app','restore-game','Restore Game','Game desc','https://game.example.test/ui','https://game.example.test/mcp',repeat('s',64),true,'owner',true,1,'approved','Restore Game v2','Game desc v2','https://game.example.test/ui-v2','https://game.example.test/mcp-v2',true,2);
INSERT INTO workspace_marketplace_apps(workspace_id,app_id,installed_by)
VALUES('restore-workspace','restore-app','owner');
INSERT INTO marketplace_app_reviews(id,app_id,revision,action,actor_id,note,web_url,mcp_url)
VALUES('restore-review','restore-app',1,'approve','admin','Initial release','https://game.example.test/ui','https://game.example.test/mcp');
SQL
  fi
  if [[ $suffix == agent_control ]]; then
    # Restore the real creation identity alongside mutable profile/reconcile state.
    for migration in services/agent-control/internal/infra/pg/migrations/*.sql; do
      "${pg[@]}" psql -U postgres -d teamie_agent_control -v ON_ERROR_STOP=1 < "$migration"
    done
    "${pg[@]}" psql -U postgres -d teamie_agent_control -v ON_ERROR_STOP=1 <<'SQL'
CREATE TABLE agent_control_schema_migrations(version INTEGER PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now());
INSERT INTO agent_control_schema_migrations(version) VALUES(1),(2),(3);
INSERT INTO agents(id,owner_id,name,model_id,workspace_ids,enabled,revision,applied_revision,creation_hash)
VALUES('restore-agent','restore-owner','Edited profile','model-one',ARRAY['restore-workspace'],true,4,4,repeat('a',64));
SQL
  fi
  if [[ $suffix == agent_runtime ]]; then
    # Exercise the actual embedded forward migrations, not a synthetic ledger.
    {
      echo "SET teamie.runtime_writer_schema='16';"
      cat services/agent-runtime/internal/infra/pg/migrations/*.sql
    } | "${pg[@]}" psql -U postgres -d teamie_agent_runtime -v ON_ERROR_STOP=1 >/dev/null
    runtime_context_budget=$(cat ops/fixtures/runtime-context-budget-v1.json)
    runtime_output_budget=$(cat ops/fixtures/runtime-context-budget-v2.json)
    runtime_current_budget=$(cat ops/fixtures/runtime-context-budget-v3.json)
    runtime_history_budget=$(cat ops/fixtures/runtime-context-budget-v4.json)
    runtime_notification_budget=$(cat ops/fixtures/runtime-context-budget-v5.json)
    runtime_long_budget=$(cat ops/fixtures/runtime-context-budget-v6.json)
    runtime_calibrated_budget=$(cat ops/fixtures/runtime-context-budget-v7.json)
    runtime_event_driven_budget=$(cat ops/fixtures/runtime-context-budget-event-driven-v1.json)
    runtime_web_budget=$(cat ops/fixtures/runtime-context-budget-web-v1.json)
    runtime_schedule_budget=$(cat ops/fixtures/runtime-context-budget-schedule-v1.json)
    "${pg[@]}" psql -U postgres -d teamie_agent_runtime -v ON_ERROR_STOP=1 -v frozen_budget="$runtime_context_budget" -v output_budget="$runtime_output_budget" -v current_budget="$runtime_current_budget" -v history_budget="$runtime_history_budget" -v notification_budget="$runtime_notification_budget" -v long_budget="$runtime_long_budget" -v calibrated_budget="$runtime_calibrated_budget" -v event_driven_budget="$runtime_event_driven_budget" -v web_budget="$runtime_web_budget" -v schedule_budget="$runtime_schedule_budget" <<'SQL'
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
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000010','10000000-0000-4000-8000-000000000001','restore-web-policy','[]',
       '30000000-0000-4000-8000-000000000010','teamie-context-v038',:'web_budget'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000011','10000000-0000-4000-8000-000000000001','restore-schedule-policy','[]',
       '30000000-0000-4000-8000-000000000011','teamie-context-v038',:'schedule_budget'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000012','10000000-0000-4000-8000-000000000001','restore-dm-policy','[]',
       '30000000-0000-4000-8000-000000000012','teamie-context-v038',:'schedule_budget'::jsonb||'{"directMessages":true}'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000013','10000000-0000-4000-8000-000000000001','restore-conversational-style-policy','[]',
       '30000000-0000-4000-8000-000000000013','teamie-context-v038',:'schedule_budget'::jsonb||'{"directMessages":true,"conversationalStyle":true}'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000014','10000000-0000-4000-8000-000000000001','restore-human-presence-v2-policy','[]',
       '30000000-0000-4000-8000-000000000014','teamie-context-v038',:'schedule_budget'::jsonb||'{"directMessages":true,"conversationalStyle":true,"humanPresenceV2":true}'::jsonb,0,0);
INSERT INTO runtime_runs(id,agent_id,input_key,input_json,attempt_id,context_profile,context_budget,context_head,acceptance_head)
VALUES('20000000-0000-4000-8000-000000000015','10000000-0000-4000-8000-000000000001','restore-human-presence-v3-policy','[]',
       '30000000-0000-4000-8000-000000000015','teamie-context-v038',:'schedule_budget'::jsonb||'{"directMessages":true,"conversationalStyle":true,"humanPresenceV2":true,"humanPresenceV3":true}'::jsonb,0,0);
INSERT INTO runtime_schedules(id,agent_id,created_run_id,kind,interval_seconds,cron_expr,timezone,prompt,status,max_runs,run_count,next_run_at,source_refs,source_overflow)
VALUES('sched-restore-fixture','10000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000011','interval',60,'','UTC','Heartbeat check','active',3,1,'2026-10-22T12:00:00Z','[{"eventId":"42","conversationId":"40000000-0000-4000-8000-000000000001","messageId":"50000000-0000-4000-8000-000000000001"}]'::jsonb,false);
INSERT INTO runtime_context_artifacts(agent_id,run_id,attempt_id,artifact_key,kind,content,content_hash,sources,source_event_seq)
VALUES('10000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','30000000-0000-4000-8000-000000000001',
       'transcript:restore','transcript',decode('00fffe80890a0d','hex'),repeat('a',64),
       '[{"eventId":"42","conversationId":"40000000-0000-4000-8000-000000000001","messageId":"50000000-0000-4000-8000-000000000001"}]',42);
INSERT INTO runtime_context_artifacts(agent_id,run_id,attempt_id,artifact_key,kind,content,content_hash,sources,source_event_seq)
VALUES('10000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000012','30000000-0000-4000-8000-000000000012',
       'workspace:restore','tool',decode('0102','hex'),repeat('c',64),
       '[{"workspaceId":"40000000-0000-4000-8000-000000000001"}]',42);
INSERT INTO runtime_context_recoveries(run_id,model_step,context_profile,request_hash,streaming,error_code,decision_version,decision_json)
VALUES('20000000-0000-4000-8000-000000000001',2,'teamie-context-v038',repeat('b',64),true,'context_overflow',1,jsonb_build_object('version',1,'action','compact_prefix','budget',:'frozen_budget'::jsonb));
SQL
  fi
  if [[ $suffix == gateway ]]; then
    for migration in services/llm-gateway/internal/infra/pg/migrations/*.sql; do
      "${pg[@]}" psql -U postgres -d teamie_gateway -v ON_ERROR_STOP=1 < "$migration" >/dev/null
    done
    "${pg[@]}" psql -U postgres -d teamie_gateway -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO gateway_providers(id,display_name,base_url,encrypted_key,enabled)
VALUES('restore-provider','Restore Provider','https://1.1.1.1/v1',decode('00fffe80890a0d','hex'),true);
INSERT INTO gateway_models(id,provider_id,display_name,description,model,context_window_tokens,enabled)
VALUES('restore-model-a','restore-provider','Model A','Primary model','gpt-restore-a',32768,true),
      ('restore-model-b','restore-provider','Model B','Secondary model','gpt-restore-b',131072,false);
INSERT INTO gateway_model_tests(model_id,ok,tested_at,latency_ms,error_code,model_updated_at,provider_updated_at)
VALUES('restore-model-a',true,'2026-11-07T00:00:00Z',42,NULL,'2026-11-07T00:00:00Z','2026-11-07T00:00:00Z'),
      ('restore-model-b',false,'2026-11-07T00:01:00Z',150,'provider_auth_failed','2026-11-07T00:00:00Z','2026-11-07T00:00:00Z');
UPDATE gateway_model_routing SET default_model_id='restore-model-a' WHERE id=true;
UPDATE gateway_web_search_config SET encrypted_key=decode('aabbccddeeff00','hex'),enabled=true WHERE id=true;
SQL
  fi
  "${pg[@]}" pg_dump -U postgres -d "teamie_$suffix" --format custom > "$scratch/$suffix.dump"
  "${pg[@]}" psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE restore_$suffix;"
  "${pg[@]}" pg_restore -U postgres -d "restore_$suffix" --exit-on-error --no-owner --no-acl < "$scratch/$suffix.dump"
  actual=$("${pg[@]}" psql -U postgres -d "restore_$suffix" -At -v ON_ERROR_STOP=1 -c \
    "SELECT (SELECT max(version) FROM ${suffix}_schema_migrations)||':'||(payload->>'message')||':'||encode(encrypted_value,'hex') FROM restore_fixture WHERE id=1;")
  [[ $actual == "$version:persisted:010203aaff" ]] || { echo "Restore verification failed for $suffix" >&2; exit 1; }
  if [[ $suffix == agent_control ]]; then
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_control -Atqc \
      "SELECT (SELECT string_agg(version::text,',' ORDER BY version) FROM agent_control_schema_migrations)||':'||creation_hash||':'||name||':'||revision||':'||applied_revision FROM agents WHERE id='restore-agent' AND owner_id='restore-owner' AND model_id='model-one' AND workspace_ids=ARRAY['restore-workspace'] AND enabled;")
    expected="1,2,3:$(printf '%064d' 0 | tr '0' 'a'):Edited profile:4:4"
    [[ $actual == "$expected" ]] || { echo 'Agent creation identity restore verification failed.' >&2; exit 1; }
  fi
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
      "SELECT (SELECT string_agg(version::text,',' ORDER BY version) FROM chat_schema_migrations)||':'||(SELECT attachment_id FROM attachment_uploads WHERE uploader_id='owner' AND upload_key='restore-upload')||':'||(SELECT count(*) FROM attachment_uploads WHERE upload_key='restore-removed' AND attachment_id IS NULL)||':'||(SELECT count(*) FROM attachment_uploads WHERE upload_key='restore-deleted-group-upload' AND conversation_id='restore-deleted-group' AND attachment_id IS NULL)||':'||(to_regclass('public.marketplace_apps') IS NULL)::text;")
    [[ $actual == '1,2,3,4,5,6,7,8,9,10,11,12,13:restore-attachment:1:1:true' ]] || { echo 'Chat upload receipts, tombstones and retired marketplace cleanup verification failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_chat -At -v ON_ERROR_STOP=1 -c \
      "SELECT (SELECT pinned_by FROM conversation_pins WHERE conversation_id='restore-conversation' AND message_id='restore-message')||':'||(SELECT count(*) FROM saved_messages WHERE user_id='owner' AND message_id='restore-message')||':'||(SELECT seq||','||manual_unread_seq FROM read_cursors WHERE conversation_id='restore-conversation' AND user_id='owner')||':'||(SELECT media_type||','||encode(content,'hex') FROM attachment_thumbnails WHERE attachment_id='restore-attachment');")
    [[ $actual == 'owner:1:2,1:image/png,89504e470d0a1a0a' ]] || { echo 'Chat collaboration pins, saved messages, manual unread and thumbnail restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_chat -At -v ON_ERROR_STOP=1 -c \
      "SELECT a.seq||':'||a.title||':'||r.read_seq FROM platform_announcements a JOIN platform_announcement_reads r ON r.user_id=a.author_id WHERE a.id='restore-announcement';")
    [[ $actual == '1:Teamie v0.4.6:1' ]] || { echo 'Chat platform announcements restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_chat -At -v ON_ERROR_STOP=1 -c \
      "SELECT request_hash||':'||conversation_id FROM conversation_mutations WHERE actor_id='owner' AND request_key='restore-group';")
    [[ $actual == 'fixture-hash:restore-conversation' ]] || { echo 'Chat conversation mutation receipt restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_chat -At -v ON_ERROR_STOP=1 -c \
      "SELECT m.reactions_version||':'||(SELECT string_agg(r.user_id,',' ORDER BY r.seq) FROM message_reactions r WHERE r.message_id=m.id AND r.emoji='🟢') FROM messages m WHERE m.id='restore-message';")
    [[ $actual == '3:owner,reader' ]] || { echo 'Chat reaction attribution and version restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_chat -At -v ON_ERROR_STOP=1 -c \
      "SELECT m.reactions_version||':'||(SELECT count(*) FROM message_reactions r WHERE r.message_id=m.id) FROM messages m WHERE m.id='restore-empty-reactions';")
    [[ $actual == '4:0' ]] || { echo 'Chat empty reaction watermark restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_chat -At -v ON_ERROR_STOP=1 -c \
      "SELECT string_agg(user_id||'='||level,',' ORDER BY user_id) FROM conversation_notification_levels WHERE conversation_id='restore-conversation';")
    [[ $actual == 'owner=mentions,reader=none' ]] || { echo 'Chat notification preference restore failed.' >&2; exit 1; }
  fi
  if [[ $suffix == marketplace ]]; then
    actual=$("${pg[@]}" psql -U postgres -d restore_marketplace -At -v ON_ERROR_STOP=1 -c \
      "SELECT (SELECT string_agg(version::text,',' ORDER BY version) FROM marketplace_schema_migrations)||':'||m.slug||':'||m.published_version||':'||m.has_draft_changes::text||':'||w.workspace_id||':'||r.action FROM marketplace_apps m JOIN workspace_marketplace_apps w ON w.app_id=m.id JOIN marketplace_app_reviews r ON r.app_id=m.id WHERE m.id='restore-app';")
    [[ $actual == '1:restore-game:1:true:restore-workspace:approve' ]] || { echo 'Marketplace app catalog, installations and review ledger restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_marketplace -At -v ON_ERROR_STOP=1 -c \
      "SELECT m.slug||':'||(m.signing_secret=repeat('m',64))::text||':'||w.review_preview::text||':'||(r.snapshot->>'migrated') FROM marketplace_apps m JOIN workspace_marketplace_apps w ON w.app_id=m.id JOIN marketplace_app_reviews r ON r.app_id=m.id WHERE m.id='legacy-migrated-app';")
    [[ $actual == 'legacy-board:true:true:true' ]] || { echo 'Transferred Chat-to-Marketplace catalog, secret, installation and review history verification failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d teamie_marketplace -At -v ON_ERROR_STOP=1 -c \
      "SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename IN ('marketplace_schema_migrations','marketplace_apps','workspace_marketplace_apps','marketplace_app_reviews') AND tableowner='teamie_marketplace';")
    [[ $actual == '4' ]] || { echo 'Marketplace migrated table ownership verification failed.' >&2; exit 1; }
  fi
  if [[ $suffix == agent_runtime ]]; then
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v frozen_budget="$runtime_context_budget" <<'SQL'
SELECT (SELECT string_agg(version::text,',' ORDER BY version) FROM agent_runtime_schema_migrations)||':'||r.context_profile||':'||(r.context_budget=:'frozen_budget'::jsonb)::text||':'||encode(a.content,'hex')||':'||(a.sources->0->>'eventId')||':'||a.source_event_seq||':'||c.model_step||':'||c.error_code||':'||c.streaming::text||':'||(c.decision_json=jsonb_build_object('version',1,'action','compact_prefix','budget',:'frozen_budget'::jsonb))::text FROM runtime_runs r JOIN runtime_context_artifacts a ON a.run_id=r.id JOIN runtime_context_recoveries c ON c.run_id=r.id WHERE r.id='20000000-0000-4000-8000-000000000001';
SQL
)
    [[ $actual == '1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16:teamie-context-v038:true:00fffe80890a0d:42:42:2:context_overflow:true:true' ]] || { echo 'Runtime frozen policy, context archive, source references and recovery tape restore failed.' >&2; exit 1; }
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
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v web_budget="$runtime_web_budget" <<'SQL'
SELECT context_budget=:'web_budget'::jsonb AND context_budget->>'webSearch'='true' FROM runtime_runs WHERE input_key='restore-web-policy';
SQL
)
    [[ $actual == t ]] || { echo 'Runtime web search policy restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v schedule_budget="$runtime_schedule_budget" <<'SQL'
SELECT (SELECT context_budget=:'schedule_budget'::jsonb AND context_budget->>'scheduledWakeups'='true' FROM runtime_runs WHERE input_key='restore-schedule-policy') AND (SELECT kind='interval' AND interval_seconds=60 AND run_count=1 AND status='active' FROM runtime_schedules WHERE id='sched-restore-fixture');
SQL
)
    [[ $actual == t ]] || { echo 'Runtime scheduled wakeups policy and schedule table restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v schedule_budget="$runtime_schedule_budget" <<'SQL'
SELECT (SELECT context_budget=(:'schedule_budget'::jsonb||'{"directMessages":true}'::jsonb) FROM runtime_runs WHERE input_key='restore-dm-policy') AND (SELECT sources='[{"workspaceId":"40000000-0000-4000-8000-000000000001"}]'::jsonb AND encode(content,'hex')='0102' FROM runtime_context_artifacts WHERE artifact_key='workspace:restore');
SQL
)
    [[ $actual == t ]] || { echo 'Runtime direct-message policy restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v schedule_budget="$runtime_schedule_budget" <<'SQL'
SELECT context_budget=(:'schedule_budget'::jsonb||'{"directMessages":true,"conversationalStyle":true}'::jsonb) FROM runtime_runs WHERE input_key='restore-conversational-style-policy';
SQL
)
    [[ $actual == t ]] || { echo 'Runtime conversational-style policy restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v schedule_budget="$runtime_schedule_budget" <<'SQL'
SELECT context_budget=(:'schedule_budget'::jsonb||'{"directMessages":true,"conversationalStyle":true,"humanPresenceV2":true}'::jsonb) FROM runtime_runs WHERE input_key='restore-human-presence-v2-policy';
SQL
)
    [[ $actual == t ]] || { echo 'Runtime human-presence-v2 policy restore failed.' >&2; exit 1; }
    actual=$("${pg[@]}" psql -U postgres -d restore_agent_runtime -At -v ON_ERROR_STOP=1 -v schedule_budget="$runtime_schedule_budget" <<'SQL'
SELECT context_budget=(:'schedule_budget'::jsonb||'{"directMessages":true,"conversationalStyle":true,"humanPresenceV2":true,"humanPresenceV3":true}'::jsonb) FROM runtime_runs WHERE input_key='restore-human-presence-v3-policy';
SQL
)
    [[ $actual == t ]] || { echo 'Runtime human-presence-v3 policy restore failed.' >&2; exit 1; }
  fi
  if [[ $suffix == gateway ]]; then
    actual=$("${pg[@]}" psql -U postgres -d restore_gateway -At -v ON_ERROR_STOP=1 -c \
      "SELECT (SELECT string_agg(version::text,',' ORDER BY version) FROM gateway_schema_migrations)||':'||encode(p.encrypted_key,'hex')||':'||(SELECT string_agg(m.id||'='||m.model||':'||m.context_window_tokens||':'||m.enabled::text,',' ORDER BY m.id) FROM gateway_models m WHERE m.provider_id=p.id)||':'||(SELECT default_model_id FROM gateway_model_routing WHERE id=true)||':'||(SELECT encode(encrypted_key,'hex')||':'||enabled::text FROM gateway_web_search_config WHERE id=true)||':'||(SELECT string_agg(model_id||'='||ok::text||':'||coalesce(error_code,'ok')||':'||latency_ms,',' ORDER BY model_id) FROM gateway_model_tests) FROM gateway_providers p WHERE p.id='restore-provider';")
    [[ $actual == '1,2,3,4,5,6,7:00fffe80890a0d:restore-model-a=gpt-restore-a:32768:true,restore-model-b=gpt-restore-b:131072:false:restore-model-a:aabbccddeeff00:true:restore-model-a=true:ok:42,restore-model-b=false:provider_auth_failed:150' ]] || { echo 'Gateway provider, multi-model catalog, routing, connection test diagnostics and encrypted secret restore verification failed.' >&2; exit 1; }
  fi
  # Ordinary owners may not connect to another service's database.
  if "${pg[@]}" env PGPASSWORD=ci-identity-disposable-password psql -h 127.0.0.1 -U teamie_identity -d teamie_gateway -c 'SELECT 1' >/dev/null 2>&1; then
    echo 'Cross-service database isolation failed.' >&2; exit 1
  fi
  "${pg[@]}" psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c "DROP DATABASE restore_$suffix;"
done

backup_bundle="$scratch/backup-bundle"
mkdir -m 0700 "$backup_bundle"
for suffix in identity chat marketplace agent_control agent_runtime gateway; do
  cp "$scratch/$suffix.dump" "$backup_bundle/teamie_$suffix.dump"
  chmod 0600 "$backup_bundle/teamie_$suffix.dump"
done

python3 - "$scratch" <<'PY'
import hashlib, json, os, pathlib, sys
scratch = pathlib.Path(sys.argv[1])
ws_root = scratch / "sandbox-workspaces"
st_root = scratch / "sandbox-state"
ws_root.mkdir(mode=0o700)
st_root.mkdir(mode=0o700)

agent_id = "10000000-0000-4000-8000-000000000001"
run_id = "20000000-0000-4000-8000-000000000001"
agent_hash = hashlib.sha256(agent_id.encode("utf-8")).hexdigest()
agent_ws = ws_root / agent_hash / "reports"
agent_ws.mkdir(parents=True, mode=0o755)
(agent_ws / "summary.md").write_text("# Restored Workspace\nAll invariants intact.\n", encoding="utf-8")
(agent_ws / "attachment.bin").write_bytes(bytes.fromhex("00fffe80890a0d"))

def task_key(aid: str, tid: str) -> str:
    return hashlib.sha256((aid + "\x00" + tid).encode("utf-8")).hexdigest()

completed_task = "restore-task-completed"
unknown_task = "restore-task-unknown"
(st_root / f"{task_key(agent_id, completed_task)}.task.json").write_text(
    json.dumps({
        "version": 2,
        "agentId": agent_id,
        "runId": run_id,
        "taskId": completed_task,
        "command": "python3 report.py",
        "fingerprint": "a" * 64,
        "containerName": "teamie-sbx-restore-1",
        "cleanupDone": True,
        "state": "completed",
        "result": {"exitCode": 0, "stdout": "done\n", "stderr": "", "truncated": False},
        "updatedAt": "2026-11-02T00:00:00Z",
    }),
    encoding="utf-8",
)
(st_root / f"{task_key(agent_id, unknown_task)}.task.json").write_text(
    json.dumps({
        "version": 2,
        "agentId": agent_id,
        "runId": run_id,
        "taskId": unknown_task,
        "command": "python3 interrupted.py",
        "fingerprint": "b" * 64,
        "containerName": "teamie-sbx-restore-2",
        "cleanupDone": False,
        "state": "unknown",
        "result": {"exitCode": -1, "stdout": "", "stderr": "", "truncated": False, "errorCode": "execution_unknown"},
        "updatedAt": "2026-11-02T00:00:00Z",
    }),
    encoding="utf-8",
)
expected_summary_hash = hashlib.sha256(b"# Restored Workspace\nAll invariants intact.\n").hexdigest()
(st_root / "mutation-restore-01.file.json").write_text(
    json.dumps({
        "version": 1,
        "fingerprint": "c" * 64,
        "state": "completed",
        "operation": "write",
        "path": "reports/summary.md",
        "expectedHash": expected_summary_hash,
        "dataJson": "{\"ok\":true}",
    }),
    encoding="utf-8",
)
os.chmod(st_root / "mutation-restore-01.file.json", 0o600)
PY

python3 ops/sandbox_archive.py archive \
  --workspaces "$scratch/sandbox-workspaces" \
  --state "$scratch/sandbox-state" \
  --out-dir "$backup_bundle" >/dev/null
python3 ops/sandbox_archive.py checksums-write --dir "$backup_bundle" >/dev/null
python3 ops/sandbox_archive.py checksums-verify --dir "$backup_bundle" >/dev/null

python3 ops/sandbox_archive.py restore \
  --archive "$backup_bundle/sandbox-workspaces.tar.gz" \
  --target "$scratch/restored-workspaces" \
  --scope workspaces >/dev/null
python3 ops/sandbox_archive.py restore \
  --archive "$backup_bundle/sandbox-state.tar.gz" \
  --target "$scratch/restored-state" \
  --scope state >/dev/null

if [[ ${EUID:-$(id -u)} -eq 0 ]]; then
  python3 - "$scratch/restored-workspaces" "$scratch/restored-state" <<'PY'
import hashlib, os, pathlib, sys
ws_root = pathlib.Path(sys.argv[1])
st_root = pathlib.Path(sys.argv[2])
agent_hash = hashlib.sha256(b"10000000-0000-4000-8000-000000000001").hexdigest()
assert (os.stat(ws_root).st_uid, os.stat(ws_root).st_gid) == (0, 0)
assert (os.stat(ws_root / agent_hash).st_uid, os.stat(ws_root / agent_hash).st_gid) == (0, 0)
assert (os.stat(ws_root / agent_hash / "reports").st_uid, os.stat(ws_root / agent_hash / "reports").st_gid) == (10001, 10001)
assert (os.stat(ws_root / agent_hash / "reports/summary.md").st_uid, os.stat(ws_root / agent_hash / "reports/summary.md").st_gid) == (10001, 10001)
assert (os.stat(st_root).st_uid, os.stat(st_root).st_gid) == (0, 0)
assert (os.stat(st_root / "mutation-restore-01.file.json").st_uid, os.stat(st_root / "mutation-restore-01.file.json").st_gid) == (0, 0)
PY
elif command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
  chmod 0755 "$scratch" "$backup_bundle"
  chmod 0644 "$backup_bundle/sandbox-workspaces.tar.gz" "$backup_bundle/sandbox-state.tar.gz"
  sudo -n python3 ops/sandbox_archive.py restore \
    --archive "$backup_bundle/sandbox-workspaces.tar.gz" \
    --target "$scratch/root-restored-workspaces" \
    --scope workspaces >/dev/null
  sudo -n python3 ops/sandbox_archive.py restore \
    --archive "$backup_bundle/sandbox-state.tar.gz" \
    --target "$scratch/root-restored-state" \
    --scope state >/dev/null
  sudo -n python3 - "$scratch/root-restored-workspaces" "$scratch/root-restored-state" <<'PY'
import hashlib, os, pathlib, shutil, sys
ws_root = pathlib.Path(sys.argv[1])
st_root = pathlib.Path(sys.argv[2])
try:
    agent_hash = hashlib.sha256(b"10000000-0000-4000-8000-000000000001").hexdigest()
    assert (os.stat(ws_root).st_uid, os.stat(ws_root).st_gid) == (0, 0)
    assert (os.stat(ws_root / agent_hash).st_uid, os.stat(ws_root / agent_hash).st_gid) == (0, 0)
    assert (os.stat(ws_root / agent_hash / "reports").st_uid, os.stat(ws_root / agent_hash / "reports").st_gid) == (10001, 10001)
    assert (os.stat(ws_root / agent_hash / "reports/summary.md").st_uid, os.stat(ws_root / agent_hash / "reports/summary.md").st_gid) == (10001, 10001)
    assert (os.stat(st_root).st_uid, os.stat(st_root).st_gid) == (0, 0)
    assert (os.stat(st_root / "mutation-restore-01.file.json").st_uid, os.stat(st_root / "mutation-restore-01.file.json").st_gid) == (0, 0)
finally:
    shutil.rmtree(ws_root, ignore_errors=True)
    shutil.rmtree(st_root, ignore_errors=True)
PY
fi

(cd services/sandbox && go build -o "$scratch/sandboxctl" ./cmd/sandboxctl)
completed_probe=$(SANDBOX_STATE_ROOT="$scratch/restored-state" "$scratch/sandboxctl" task \
  --agent 10000000-0000-4000-8000-000000000001 \
  --run 20000000-0000-4000-8000-000000000001 \
  --task restore-task-completed)
unknown_probe=$(SANDBOX_STATE_ROOT="$scratch/restored-state" "$scratch/sandboxctl" task \
  --agent 10000000-0000-4000-8000-000000000001 \
  --run 20000000-0000-4000-8000-000000000001 \
  --task restore-task-unknown)

python3 - "$scratch/restored-workspaces" "$scratch/restored-state" "$completed_probe" "$unknown_probe" <<'PY'
import hashlib, json, pathlib, sys
ws_root = pathlib.Path(sys.argv[1])
st_root = pathlib.Path(sys.argv[2])
completed = json.loads(sys.argv[3])
unknown = json.loads(sys.argv[4])
assert completed["state"] == "completed" and completed["success"] is True, completed
assert unknown["state"] == "unknown" and unknown["success"] is False, unknown
agent_hash = hashlib.sha256(b"10000000-0000-4000-8000-000000000001").hexdigest()
assert (ws_root / agent_hash / "reports/attachment.bin").read_bytes() == bytes.fromhex("00fffe80890a0d")
file_rec = json.loads((st_root / "mutation-restore-01.file.json").read_text(encoding="utf-8"))
actual_summary_hash = hashlib.sha256((ws_root / agent_hash / "reports/summary.md").read_bytes()).hexdigest()
assert file_rec["state"] == "completed" and file_rec["expectedHash"] == actual_summary_hash
PY

cfg_root="$scratch/cfg-host"
mkdir -p "$cfg_root/config" "$cfg_root/backups"
printf 'GATEWAY_MASTER_KEY=%s\nAGENT_RUNTIME_TOKEN=%s\n' "$(printf '%064d' 0 | tr '0' 'e')" "ci-disposable-runtime-token-123456" > "$cfg_root/config/production.env"
chmod 0600 "$cfg_root/config/production.env"
cp deploy/feature-flags.default.json "$cfg_root/config/feature-flags.json"
chmod 0644 "$cfg_root/config/feature-flags.json"
printf 'ci-restore-passphrase-at-least-sixteen-bytes\n' > "$scratch/passphrase.key"
chmod 0600 "$scratch/passphrase.key"
TEAMIE_ROOT="$cfg_root" bash ops/backup-config-encrypted.sh \
  --out "$scratch/offhost-config.enc" --passphrase-file "$scratch/passphrase.key" >/dev/null
TEAMIE_ROOT="$cfg_root" bash ops/backup-config-encrypted.sh \
  --decrypt --in "$scratch/offhost-config.enc" --out-dir "$scratch/restored-config" \
  --passphrase-file "$scratch/passphrase.key" >/dev/null
cmp -s "$cfg_root/config/production.env" "$scratch/restored-config/production.env" || { echo 'Encrypted production.env restore mismatch.' >&2; exit 1; }
cmp -s "$cfg_root/config/feature-flags.json" "$scratch/restored-config/feature-flags.json" || { echo 'Encrypted feature-flags.json restore mismatch.' >&2; exit 1; }

echo 'All six disposable database dumps, Sandbox workspaces/receipts and encrypted configuration bundles were restored and verified.'
