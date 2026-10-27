ALTER TABLE messages ADD COLUMN IF NOT EXISTS reactions_version bigint NOT NULL DEFAULT 0
  CHECK (reactions_version >= 0);

CREATE TABLE IF NOT EXISTS message_reactions (
  conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  message_id text NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  emoji text NOT NULL,
  user_id text NOT NULL,
  seq bigserial NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (message_id, emoji, user_id)
);

CREATE INDEX IF NOT EXISTS message_reactions_message_seq_idx
  ON message_reactions(message_id, seq);

CREATE TABLE IF NOT EXISTS conversation_notification_levels (
  conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_id text NOT NULL,
  level text NOT NULL CHECK (level IN ('all', 'mentions', 'none')),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (conversation_id, user_id)
);
