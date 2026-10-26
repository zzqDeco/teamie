-- Receipts commit atomically with generic IM conversation mutations. Retain IDs
-- after membership changes so retries never restore a removed participant.
CREATE TABLE IF NOT EXISTS conversation_mutations (
 actor_id text NOT NULL,
 request_key text NOT NULL,
 request_hash text NOT NULL,
 conversation_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (actor_id, request_key)
);
