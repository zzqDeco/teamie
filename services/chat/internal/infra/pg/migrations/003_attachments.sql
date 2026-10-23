-- Binary content is Chat-owned and included in the existing Chat pg_dump.
-- Message projections retain metadata only; history never selects bytea.
CREATE TABLE IF NOT EXISTS attachments (
 id text PRIMARY KEY,
 conversation_id text NOT NULL REFERENCES conversations(id),
 uploader_id text NOT NULL,
 message_id text REFERENCES messages(id),
 filename text NOT NULL,
 media_type text NOT NULL,
 size_bytes integer NOT NULL CHECK(size_bytes BETWEEN 1 AND 4194304),
 sha256 text NOT NULL,
 content bytea NOT NULL CHECK(octet_length(content)=size_bytes),
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours'
);
CREATE INDEX IF NOT EXISTS attachments_pending ON attachments(expires_at) WHERE message_id IS NULL;
CREATE INDEX IF NOT EXISTS attachments_uploader ON attachments(uploader_id) WHERE message_id IS NULL;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS attachments jsonb NOT NULL DEFAULT '[]';
ALTER TABLE messages ADD COLUMN IF NOT EXISTS original_attachment_ids text[] NOT NULL DEFAULT '{}';
