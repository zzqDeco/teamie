-- Schema 13: retain sender-scoped upload_key receipts after group dissolution
-- so spent upload keys cannot be replayed against another conversation.
ALTER TABLE attachment_uploads
  DROP CONSTRAINT IF EXISTS attachment_uploads_conversation_id_fkey;
