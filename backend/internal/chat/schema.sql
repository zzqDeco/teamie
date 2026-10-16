CREATE TABLE IF NOT EXISTS workspaces (
 id text PRIMARY KEY,name text NOT NULL,created_by text NOT NULL,created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS workspace_members (
 workspace_id text NOT NULL REFERENCES workspaces(id),user_id text NOT NULL,role text NOT NULL CHECK(role IN ('owner','member')),
 PRIMARY KEY(workspace_id,user_id)
);
CREATE TABLE IF NOT EXISTS conversations (
 id text PRIMARY KEY,workspace_id text NOT NULL REFERENCES workspaces(id),type text NOT NULL CHECK(type IN ('channel','direct','group')),
 name text NOT NULL,pair_key text,last_seq bigint NOT NULL DEFAULT 0,updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS conversations_direct_pair ON conversations(workspace_id,pair_key) WHERE pair_key IS NOT NULL;
CREATE TABLE IF NOT EXISTS conversation_members (
 conversation_id text NOT NULL REFERENCES conversations(id),user_id text NOT NULL,PRIMARY KEY(conversation_id,user_id)
);
CREATE TABLE IF NOT EXISTS messages (
 id text PRIMARY KEY,conversation_id text NOT NULL REFERENCES conversations(id),sender_id text NOT NULL,sender_name text NOT NULL,sender_kind text NOT NULL,
 client_message_id text NOT NULL,seq bigint NOT NULL,body text NOT NULL,original_body_hash text NOT NULL,reply_to_id text REFERENCES messages(id),
 created_at timestamptz NOT NULL DEFAULT now(),edited_at timestamptz,retracted_at timestamptz,
 UNIQUE(sender_id,client_message_id),UNIQUE(conversation_id,seq)
);
CREATE TABLE IF NOT EXISTS read_cursors (
 conversation_id text NOT NULL REFERENCES conversations(id),user_id text NOT NULL,seq bigint NOT NULL,PRIMARY KEY(conversation_id,user_id)
);
CREATE TABLE IF NOT EXISTS workspace_invites (
 code_hash text PRIMARY KEY,workspace_id text NOT NULL REFERENCES workspaces(id),created_by text NOT NULL,expires_at timestamptz NOT NULL
);
-- A head is locked until commit. Per-user cursors cannot skip concurrent commits.
CREATE TABLE IF NOT EXISTS sync_heads (user_id text PRIMARY KEY,last_id bigint NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS sync_events (
 user_id text NOT NULL,id bigint NOT NULL,workspace_id text,conversation_id text,payload jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(user_id,id)
);
CREATE TABLE IF NOT EXISTS outbox (
 id bigserial PRIMARY KEY,payload jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),published_at timestamptz
);
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox(id) WHERE published_at IS NULL;

CREATE TABLE IF NOT EXISTS chat_schema_migrations (version integer PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now());
INSERT INTO chat_schema_migrations(version) VALUES(1) ON CONFLICT DO NOTHING;
