CREATE TABLE IF NOT EXISTS marketplace_apps (
  id text PRIMARY KEY,
  slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
  name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
  description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
  web_url text NOT NULL DEFAULT '' CHECK (web_url = '' OR web_url ~ '^https?://'),
  mcp_url text NOT NULL DEFAULT '' CHECK (mcp_url = '' OR mcp_url ~ '^https?://'),
  signing_secret text NOT NULL CHECK (char_length(signing_secret) >= 32),
  enabled boolean NOT NULL DEFAULT true,
  owner_id text NULL,
  is_published boolean NOT NULL DEFAULT false,
  published_version integer NOT NULL DEFAULT 0,
  review_status text NOT NULL DEFAULT 'draft' CHECK (review_status IN ('draft', 'pending_review', 'approved', 'rejected')),
  draft_name text NOT NULL CHECK (char_length(btrim(draft_name)) BETWEEN 1 AND 80),
  draft_description text NOT NULL DEFAULT '' CHECK (char_length(draft_description) <= 500),
  draft_web_url text NOT NULL DEFAULT '' CHECK (draft_web_url = '' OR draft_web_url ~ '^https?://'),
  draft_mcp_url text NOT NULL DEFAULT '' CHECK (draft_mcp_url = '' OR draft_mcp_url ~ '^https?://'),
  has_draft_changes boolean NOT NULL DEFAULT false,
  submission_revision integer NOT NULL DEFAULT 0,
  submission_note text NOT NULL DEFAULT '' CHECK (char_length(submission_note) <= 1000),
  submitted_at timestamptz NULL,
  reviewed_by text NULL,
  reviewed_at timestamptz NULL,
  review_note text NOT NULL DEFAULT '' CHECK (char_length(review_note) <= 1000),
  admin_suspended boolean NOT NULL DEFAULT false,
  suspended_reason text NOT NULL DEFAULT '' CHECK (char_length(suspended_reason) <= 500),
  suspended_at timestamptz NULL,
  last_verified_at timestamptz NULL,
  verification_report jsonb NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT marketplace_apps_published_urls_check CHECK (NOT is_published OR (web_url ~ '^https?://' AND mcp_url ~ '^https?://')),
  CONSTRAINT marketplace_apps_pending_urls_check CHECK (review_status <> 'pending_review' OR (draft_web_url ~ '^https?://' AND draft_mcp_url ~ '^https?://'))
);

CREATE INDEX IF NOT EXISTS marketplace_apps_owner_idx
  ON marketplace_apps(owner_id, created_at DESC)
  WHERE owner_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS marketplace_apps_review_status_idx
  ON marketplace_apps(review_status, submitted_at ASC);

CREATE TABLE IF NOT EXISTS workspace_marketplace_apps (
  workspace_id text NOT NULL,
  app_id text NOT NULL REFERENCES marketplace_apps(id) ON DELETE CASCADE,
  installed_by text NOT NULL,
  review_preview boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, app_id)
);

CREATE INDEX IF NOT EXISTS workspace_marketplace_apps_app_idx
  ON workspace_marketplace_apps(app_id);

CREATE TABLE IF NOT EXISTS marketplace_app_reviews (
  id text PRIMARY KEY,
  app_id text NOT NULL REFERENCES marketplace_apps(id) ON DELETE CASCADE,
  revision integer NOT NULL DEFAULT 0,
  action text NOT NULL CHECK (action IN ('submit', 'withdraw', 'approve', 'reject', 'suspend', 'unsuspend')),
  actor_id text NOT NULL,
  note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 1000),
  web_url text NOT NULL DEFAULT '',
  mcp_url text NOT NULL DEFAULT '',
  snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS marketplace_app_reviews_app_idx
  ON marketplace_app_reviews(app_id, created_at DESC, id DESC);
