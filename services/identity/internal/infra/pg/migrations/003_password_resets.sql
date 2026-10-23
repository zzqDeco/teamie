CREATE TABLE identity_password_resets (
 id text PRIMARY KEY,
 user_id text NOT NULL REFERENCES identity_users(id) ON DELETE CASCADE,
 token_hash text NOT NULL UNIQUE,
 created_by text REFERENCES identity_users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 used_at timestamptz,
 revoked_at timestamptz,
 CHECK (expires_at > created_at),
 CHECK (used_at IS NULL OR revoked_at IS NULL)
);
-- Expired links are also retired when another link is issued. Keeping expiry
-- out of the predicate makes the one-active-link invariant enforceable by PG.
CREATE UNIQUE INDEX identity_password_resets_active_user
 ON identity_password_resets(user_id) WHERE used_at IS NULL AND revoked_at IS NULL;
CREATE INDEX identity_users_admin_list ON identity_users(id)
 WHERE kind='human' AND enabled AND deleted_at IS NULL;
