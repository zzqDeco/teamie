-- Generic upload receipts survive pending cleanup, deletion and retraction.
-- They contain no file bytes or Agent/runtime metadata.
CREATE TABLE IF NOT EXISTS attachment_uploads (
 uploader_id text NOT NULL,
 upload_key text NOT NULL,
 conversation_id text NOT NULL REFERENCES conversations(id),
 fingerprint text NOT NULL,
 attachment_id text UNIQUE REFERENCES attachments(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (uploader_id,upload_key)
);
