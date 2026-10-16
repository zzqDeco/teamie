ALTER TABLE identity_users DROP CONSTRAINT identity_users_kind_check;
ALTER TABLE identity_users ADD CONSTRAINT identity_users_kind_check CHECK(kind IN ('human','agent'));
ALTER TABLE identity_users ALTER COLUMN email DROP NOT NULL;
ALTER TABLE identity_users ALTER COLUMN password_hash DROP NOT NULL;
ALTER TABLE identity_users ADD COLUMN enabled boolean NOT NULL DEFAULT true;
ALTER TABLE identity_users ADD COLUMN deleted_at timestamptz;
ALTER TABLE identity_users ADD COLUMN principal_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE identity_users ADD CONSTRAINT identity_users_credentials_check CHECK(
 (kind='human' AND email IS NOT NULL AND password_hash IS NOT NULL) OR
 (kind='agent' AND email IS NULL AND password_hash IS NULL)
);
CREATE TABLE principal_tokens (
 principal_id text PRIMARY KEY REFERENCES identity_users(id),token_hash text NOT NULL UNIQUE,
 expires_at timestamptz NOT NULL,created_at timestamptz NOT NULL DEFAULT now()
);
