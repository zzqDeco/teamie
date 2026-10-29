CREATE TABLE IF NOT EXISTS marketplace_apps (
  id text PRIMARY KEY,
  slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
  name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
  web_url text NOT NULL CHECK (web_url ~ '^https?://'),
  mcp_url text NOT NULL CHECK (mcp_url ~ '^https?://'),
  signing_secret text NOT NULL CHECK (char_length(signing_secret) >= 32),
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspace_marketplace_apps (
  workspace_id text NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  app_id text NOT NULL REFERENCES marketplace_apps(id) ON DELETE CASCADE,
  installed_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, app_id)
);

CREATE INDEX IF NOT EXISTS workspace_marketplace_apps_app_idx
  ON workspace_marketplace_apps(app_id);
