CREATE TABLE IF NOT EXISTS identity_users (
 id text PRIMARY KEY, email text NOT NULL UNIQUE,
 display_name text NOT NULL, kind text NOT NULL DEFAULT 'human' CHECK (kind='human'),
 password_hash text NOT NULL, platform_role text NOT NULL DEFAULT 'user' CHECK (platform_role IN ('admin','user')),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS identity_users_email_lower ON identity_users(lower(email));
CREATE TABLE IF NOT EXISTS identity_sessions (
 token_hash text PRIMARY KEY, user_id text NOT NULL REFERENCES identity_users(id) ON DELETE CASCADE,
 csrf_token text NOT NULL, expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS identity_sessions_expiry ON identity_sessions(expires_at);
CREATE TABLE IF NOT EXISTS registration_invites (
 id text PRIMARY KEY,code_hash text NOT NULL UNIQUE,
 created_by text NOT NULL REFERENCES identity_users(id),
 created_at timestamptz NOT NULL DEFAULT now(),expires_at timestamptz NOT NULL,
 used_at timestamptz,used_by text REFERENCES identity_users(id),revoked_at timestamptz,
 CHECK ((used_at IS NULL) = (used_by IS NULL))
);

CREATE TABLE IF NOT EXISTS identity_schema_migrations (version integer PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now());
INSERT INTO identity_schema_migrations(version) VALUES(1) ON CONFLICT DO NOTHING;
