CREATE TABLE IF NOT EXISTS push_schema_migrations(
  version INTEGER PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS push_subscriptions(
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  endpoint_hash TEXT NOT NULL UNIQUE,
  endpoint TEXT NOT NULL,
  endpoint_host TEXT NOT NULL,
  p256dh TEXT NOT NULL,
  auth TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_success_at TIMESTAMPTZ,
  last_error_code TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS push_subscriptions_user_updated_idx
  ON push_subscriptions(user_id, updated_at DESC, id DESC);
