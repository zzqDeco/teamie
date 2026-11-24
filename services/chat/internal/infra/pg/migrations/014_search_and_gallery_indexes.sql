-- Schema 14: indexes for cross-conversation search and conversation attachment gallery.
-- pg_trgm is a PostgreSQL trusted contrib extension (no superuser required for DB owner).
-- Install explicitly into public so per-schema test isolation (search_path=test_*) does
-- not drop the extension or hide gin_trgm_ops across concurrent test runs.
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

-- Accelerates user-scoped membership lookups for cross-workspace search and attention queries.
CREATE INDEX IF NOT EXISTS conversation_members_user_idx
  ON conversation_members (user_id, conversation_id);

-- Accelerates active message time-ordered pagination and date-range pruning per conversation.
CREATE INDEX IF NOT EXISTS messages_active_conv_created_idx
  ON messages (conversation_id, created_at DESC, id DESC)
  WHERE retracted_at IS NULL;

-- Accelerates substring/trigram search over unretracted message bodies and forwarded snapshots.
CREATE INDEX IF NOT EXISTS messages_search_trgm_idx
  ON messages
  USING GIN (lower(body || ' ' || COALESCE(forwarded->>'body', '')) public.gin_trgm_ops)
  WHERE retracted_at IS NULL;

-- Accelerates published attachment gallery listing per conversation (covers direct & forwarded copies).
CREATE INDEX IF NOT EXISTS attachments_conv_msg_idx
  ON attachments (conversation_id, message_id, id DESC)
  WHERE message_id IS NOT NULL;
