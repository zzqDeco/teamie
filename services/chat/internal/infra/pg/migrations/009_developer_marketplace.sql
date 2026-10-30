ALTER TABLE marketplace_apps
  DROP CONSTRAINT IF EXISTS marketplace_apps_web_url_check,
  DROP CONSTRAINT IF EXISTS marketplace_apps_mcp_url_check;

ALTER TABLE marketplace_apps
  ALTER COLUMN web_url SET DEFAULT '',
  ALTER COLUMN mcp_url SET DEFAULT '',
  ADD COLUMN IF NOT EXISTS owner_id text NULL,
  ADD COLUMN IF NOT EXISTS is_published boolean NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS published_version integer NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS review_status text NOT NULL DEFAULT 'approved',
  ADD COLUMN IF NOT EXISTS draft_name text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS draft_description text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS draft_web_url text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS draft_mcp_url text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS has_draft_changes boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS submission_revision integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS submission_note text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS submitted_at timestamptz NULL,
  ADD COLUMN IF NOT EXISTS reviewed_by text NULL,
  ADD COLUMN IF NOT EXISTS reviewed_at timestamptz NULL,
  ADD COLUMN IF NOT EXISTS review_note text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS admin_suspended boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS suspended_reason text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS suspended_at timestamptz NULL,
  ADD COLUMN IF NOT EXISTS last_verified_at timestamptz NULL,
  ADD COLUMN IF NOT EXISTS verification_report jsonb NULL;

UPDATE marketplace_apps
SET draft_name = CASE WHEN draft_name = '' THEN name ELSE draft_name END,
    draft_description = CASE WHEN draft_description = '' AND description <> '' THEN description ELSE draft_description END,
    draft_web_url = CASE WHEN draft_web_url = '' THEN web_url ELSE draft_web_url END,
    draft_mcp_url = CASE WHEN draft_mcp_url = '' THEN mcp_url ELSE draft_mcp_url END;

ALTER TABLE marketplace_apps
  ALTER COLUMN is_published SET DEFAULT false,
  ALTER COLUMN published_version SET DEFAULT 0,
  ALTER COLUMN review_status SET DEFAULT 'draft';

ALTER TABLE marketplace_apps
  ADD CONSTRAINT marketplace_apps_web_url_check CHECK (web_url = '' OR web_url ~ '^https?://'),
  ADD CONSTRAINT marketplace_apps_mcp_url_check CHECK (mcp_url = '' OR mcp_url ~ '^https?://'),
  ADD CONSTRAINT marketplace_apps_published_urls_check CHECK (NOT is_published OR (web_url ~ '^https?://' AND mcp_url ~ '^https?://')),
  ADD CONSTRAINT marketplace_apps_draft_name_check CHECK (char_length(btrim(draft_name)) BETWEEN 1 AND 80),
  ADD CONSTRAINT marketplace_apps_draft_desc_check CHECK (char_length(draft_description) <= 500),
  ADD CONSTRAINT marketplace_apps_draft_web_url_check CHECK (draft_web_url = '' OR draft_web_url ~ '^https?://'),
  ADD CONSTRAINT marketplace_apps_draft_mcp_url_check CHECK (draft_mcp_url = '' OR draft_mcp_url ~ '^https?://'),
  ADD CONSTRAINT marketplace_apps_review_status_check CHECK (review_status IN ('draft', 'pending_review', 'approved', 'rejected')),
  ADD CONSTRAINT marketplace_apps_pending_urls_check CHECK (review_status <> 'pending_review' OR (draft_web_url ~ '^https?://' AND draft_mcp_url ~ '^https?://')),
  ADD CONSTRAINT marketplace_apps_submission_note_check CHECK (char_length(submission_note) <= 1000),
  ADD CONSTRAINT marketplace_apps_review_note_check CHECK (char_length(review_note) <= 1000),
  ADD CONSTRAINT marketplace_apps_suspended_reason_check CHECK (char_length(suspended_reason) <= 500);

CREATE INDEX IF NOT EXISTS marketplace_apps_owner_idx
  ON marketplace_apps(owner_id, created_at DESC)
  WHERE owner_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS marketplace_apps_review_status_idx
  ON marketplace_apps(review_status, submitted_at ASC);

ALTER TABLE workspace_marketplace_apps
  ADD COLUMN IF NOT EXISTS review_preview boolean NOT NULL DEFAULT false;

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
