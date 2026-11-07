-- Schema 12: one forward-only migration for the IM collaboration release
-- (forwarding, broadcast mentions, pins, saved items, manual unread markers and
-- image thumbnails). It only adds nullable/defaulted columns and new tables and
-- rewrites no existing message, sequence, attachment, receipt, annotation,
-- reaction or announcement data.

-- Forwarding: the immutable forwardOf identity used by retry comparisons survives
-- retraction; the display snapshot (original sender, source IDs, body,
-- display-only mentions and copied attachment descriptors) is removed on
-- retraction. Source IDs are plain references, never foreign keys or grants.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS forward_source_conversation_id text;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS forward_source_message_id text;
ALTER TABLE messages ADD COLUMN IF NOT EXISTS forwarded jsonb;
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_forward_source_pair;
ALTER TABLE messages ADD CONSTRAINT messages_forward_source_pair
  CHECK ((forward_source_conversation_id IS NULL) = (forward_source_message_id IS NULL));

-- Copied forward attachments are ordinary target-conversation rows linked to the
-- forward message; copied_from records the server-side copy relationship only.
ALTER TABLE attachments ADD COLUMN IF NOT EXISTS copied_from text;

-- Broadcast mentions: entries live in the existing current/original mention
-- arrays; the resolved recipient snapshot of the current broadcast entry stays
-- server-side and is never returned for `here`.
ALTER TABLE messages ADD COLUMN IF NOT EXISTS broadcast_recipient_ids text[] NOT NULL DEFAULT '{}';

-- Pins: conversation-scoped, attributed to the pinner at pin time.
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS pins_version bigint NOT NULL DEFAULT 0
  CHECK (pins_version >= 0);
CREATE TABLE IF NOT EXISTS conversation_pins (
  conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  message_id text NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  pinned_by text NOT NULL,
  pinned_by_name text NOT NULL,
  pinned_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (conversation_id, message_id)
);
CREATE INDEX IF NOT EXISTS conversation_pins_order_idx
  ON conversation_pins(conversation_id, pinned_at DESC, message_id DESC);

-- Saved items: private to each caller.
CREATE TABLE IF NOT EXISTS saved_messages (
  user_id text NOT NULL,
  conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  message_id text NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  saved_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, message_id)
);
CREATE INDEX IF NOT EXISTS saved_messages_user_order_idx
  ON saved_messages(user_id, saved_at DESC, message_id DESC);
CREATE INDEX IF NOT EXISTS saved_messages_conversation_user_idx
  ON saved_messages(conversation_id, user_id);
CREATE INDEX IF NOT EXISTS saved_messages_message_idx
  ON saved_messages(message_id);

-- Manual unread: a nullable per-member marker beside the monotonic read cursor.
ALTER TABLE read_cursors ADD COLUMN IF NOT EXISTS manual_unread_seq bigint
  CHECK (manual_unread_seq IS NULL OR manual_unread_seq >= 1);

-- Image metadata and thumbnails: dimensions are recorded iff a thumbnail exists.
ALTER TABLE attachments ADD COLUMN IF NOT EXISTS image_width integer
  CHECK (image_width IS NULL OR image_width BETWEEN 1 AND 16384);
ALTER TABLE attachments ADD COLUMN IF NOT EXISTS image_height integer
  CHECK (image_height IS NULL OR image_height BETWEEN 1 AND 16384);
CREATE TABLE IF NOT EXISTS attachment_thumbnails (
  attachment_id text PRIMARY KEY REFERENCES attachments(id) ON DELETE CASCADE,
  media_type text NOT NULL CHECK (media_type IN ('image/jpeg', 'image/png')),
  content bytea NOT NULL CHECK (octet_length(content) BETWEEN 1 AND 262144),
  created_at timestamptz NOT NULL DEFAULT now()
);
