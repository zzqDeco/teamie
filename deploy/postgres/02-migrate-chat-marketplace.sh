#!/usr/bin/env bash
# Forward-only data transfer from embedded Chat Marketplace tables (schema 8/9)
# into standalone teamie_marketplace (schema 1) before Chat migration 010 drops
# the retired tables. Safe to re-run; no-op when Chat has no legacy tables.
set -euo pipefail
umask 077
export PGPASSWORD="${POSTGRES_PASSWORD:?postgres superuser password required}"

psql_super=(psql --username "${POSTGRES_SUPERUSER:-postgres}" --tuples-only --no-align --set ON_ERROR_STOP=1)
chat_db=${TEAMIE_CHAT_DB:-teamie_chat}
marketplace_db=${TEAMIE_MARKETPLACE_DB:-teamie_marketplace}
marketplace_role=${TEAMIE_MARKETPLACE_ROLE:-teamie_marketplace}

if [[ ! $marketplace_role =~ ^[a-z_][a-z0-9_]*$ ]]; then
  echo "Invalid marketplace database role identifier: $marketplace_role" >&2
  exit 1
fi

has_legacy_apps=$("${psql_super[@]}" --dbname "$chat_db" --command "SELECT to_regclass('public.marketplace_apps') IS NOT NULL")
if [[ $has_legacy_apps != t ]]; then
  exit 0
fi

# Clear any prior transfer marker before inspecting or copying so an interrupted
# retry after source mutations can never leave a stale stamp behind.
"${psql_super[@]}" --dbname "$chat_db" --command "COMMENT ON TABLE public.marketplace_apps IS NULL;" >/dev/null

scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT

has_schema_ledger=$("${psql_super[@]}" --dbname "$marketplace_db" --command "SELECT to_regclass('public.marketplace_schema_migrations') IS NOT NULL")
if [[ $has_schema_ledger == t ]]; then
  schema_version=$("${psql_super[@]}" --dbname "$marketplace_db" --command "SELECT COALESCE(max(version)::text, '0') FROM public.marketplace_schema_migrations")
else
  schema_version=0
fi

if [[ $schema_version == 0 ]]; then
  {
    printf 'BEGIN;\nSELECT pg_advisory_xact_lock(704801);\nSET LOCAL ROLE %s;\n' "$marketplace_role"
    cat <<'SQL'
CREATE TABLE IF NOT EXISTS marketplace_schema_migrations (
  version integer PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);
-- BEGIN_MARKETPLACE_SCHEMA_V1
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
-- END_MARKETPLACE_SCHEMA_V1
INSERT INTO marketplace_schema_migrations(version) VALUES (1) ON CONFLICT DO NOTHING;
COMMIT;
SQL
  } | psql --username "${POSTGRES_SUPERUSER:-postgres}" --dbname "$marketplace_db" --set ON_ERROR_STOP=1 >/dev/null
fi

has_v9_cols=$("${psql_super[@]}" --dbname "$chat_db" --command "
  SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'marketplace_apps' AND column_name = 'owner_id'
  )
")
has_installs_table=$("${psql_super[@]}" --dbname "$chat_db" --command "SELECT to_regclass('public.workspace_marketplace_apps') IS NOT NULL")
has_installs_preview=$("${psql_super[@]}" --dbname "$chat_db" --command "
  SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'workspace_marketplace_apps' AND column_name = 'review_preview'
  )
")
has_reviews_table=$("${psql_super[@]}" --dbname "$chat_db" --command "SELECT to_regclass('public.marketplace_app_reviews') IS NOT NULL")

if [[ $has_v9_cols == t ]]; then
  "${psql_super[@]}" --dbname "$chat_db" --command "
    COPY (
      SELECT
        id, slug, name, description, web_url, mcp_url, signing_secret, enabled,
        owner_id, is_published, published_version, review_status,
        draft_name, draft_description, draft_web_url, draft_mcp_url,
        has_draft_changes, submission_revision, submission_note, submitted_at,
        reviewed_by, reviewed_at, review_note,
        admin_suspended, suspended_reason, suspended_at,
        last_verified_at, verification_report, created_at, updated_at
      FROM public.marketplace_apps
      ORDER BY created_at ASC, id ASC
    ) TO STDOUT WITH (FORMAT binary)
  " > "$scratch/apps.bin"
else
  "${psql_super[@]}" --dbname "$chat_db" --command "
    COPY (
      SELECT
        id, slug, name, description, web_url, mcp_url, signing_secret, enabled,
        NULL::text AS owner_id,
        true AS is_published,
        1 AS published_version,
        'approved'::text AS review_status,
        name AS draft_name,
        description AS draft_description,
        web_url AS draft_web_url,
        mcp_url AS draft_mcp_url,
        false AS has_draft_changes,
        0 AS submission_revision,
        ''::text AS submission_note,
        NULL::timestamptz AS submitted_at,
        NULL::text AS reviewed_by,
        NULL::timestamptz AS reviewed_at,
        ''::text AS review_note,
        false AS admin_suspended,
        ''::text AS suspended_reason,
        NULL::timestamptz AS suspended_at,
        NULL::timestamptz AS last_verified_at,
        NULL::jsonb AS verification_report,
        created_at,
        updated_at
      FROM public.marketplace_apps
      ORDER BY created_at ASC, id ASC
    ) TO STDOUT WITH (FORMAT binary)
  " > "$scratch/apps.bin"
fi

if [[ $has_installs_table == t && $has_installs_preview == t ]]; then
  "${psql_super[@]}" --dbname "$chat_db" --command "
    COPY (
      SELECT workspace_id, app_id, installed_by, review_preview, created_at
      FROM public.workspace_marketplace_apps
      ORDER BY created_at ASC, workspace_id ASC, app_id ASC
    ) TO STDOUT WITH (FORMAT binary)
  " > "$scratch/installs.bin"
elif [[ $has_installs_table == t ]]; then
  "${psql_super[@]}" --dbname "$chat_db" --command "
    COPY (
      SELECT workspace_id, app_id, installed_by, false AS review_preview, created_at
      FROM public.workspace_marketplace_apps
      ORDER BY created_at ASC, workspace_id ASC, app_id ASC
    ) TO STDOUT WITH (FORMAT binary)
  " > "$scratch/installs.bin"
else
  "${psql_super[@]}" --dbname "$chat_db" --command "
    COPY (
      SELECT NULL::text, NULL::text, NULL::text, false, NULL::timestamptz WHERE false
    ) TO STDOUT WITH (FORMAT binary)
  " > "$scratch/installs.bin"
fi

if [[ $has_reviews_table == t ]]; then
  "${psql_super[@]}" --dbname "$chat_db" --command "
    COPY (
      SELECT id, app_id, revision, action, actor_id, note, web_url, mcp_url, snapshot, created_at
      FROM public.marketplace_app_reviews
      ORDER BY created_at ASC, id ASC
    ) TO STDOUT WITH (FORMAT binary)
  " > "$scratch/reviews.bin"
else
  "${psql_super[@]}" --dbname "$chat_db" --command "
    COPY (
      SELECT NULL::text, NULL::text, 0::integer, NULL::text, NULL::text, NULL::text, NULL::text, NULL::text, NULL::jsonb, NULL::timestamptz WHERE false
    ) TO STDOUT WITH (FORMAT binary)
  " > "$scratch/reviews.bin"
fi

(
  cd "$scratch"
  {
    printf 'BEGIN;\nSELECT pg_advisory_xact_lock(704801);\nSET LOCAL ROLE %s;\n' "$marketplace_role"
    cat <<'SQL'
CREATE TEMP TABLE stage_apps (LIKE public.marketplace_apps INCLUDING DEFAULTS) ON COMMIT DROP;
CREATE TEMP TABLE stage_installs (LIKE public.workspace_marketplace_apps INCLUDING DEFAULTS) ON COMMIT DROP;
CREATE TEMP TABLE stage_reviews (LIKE public.marketplace_app_reviews INCLUDING DEFAULTS) ON COMMIT DROP;

\copy stage_apps (id, slug, name, description, web_url, mcp_url, signing_secret, enabled, owner_id, is_published, published_version, review_status, draft_name, draft_description, draft_web_url, draft_mcp_url, has_draft_changes, submission_revision, submission_note, submitted_at, reviewed_by, reviewed_at, review_note, admin_suspended, suspended_reason, suspended_at, last_verified_at, verification_report, created_at, updated_at) FROM 'apps.bin' WITH (FORMAT binary)
\copy stage_installs (workspace_id, app_id, installed_by, review_preview, created_at) FROM 'installs.bin' WITH (FORMAT binary)
\copy stage_reviews (id, app_id, revision, action, actor_id, note, web_url, mcp_url, snapshot, created_at) FROM 'reviews.bin' WITH (FORMAT binary)

INSERT INTO public.marketplace_apps (
  id, slug, name, description, web_url, mcp_url, signing_secret, enabled,
  owner_id, is_published, published_version, review_status,
  draft_name, draft_description, draft_web_url, draft_mcp_url,
  has_draft_changes, submission_revision, submission_note, submitted_at,
  reviewed_by, reviewed_at, review_note,
  admin_suspended, suspended_reason, suspended_at,
  last_verified_at, verification_report, created_at, updated_at
)
SELECT
  id, slug, name, description, web_url, mcp_url, signing_secret, enabled,
  owner_id, is_published, published_version, review_status,
  draft_name, draft_description, draft_web_url, draft_mcp_url,
  has_draft_changes, submission_revision, submission_note, submitted_at,
  reviewed_by, reviewed_at, review_note,
  admin_suspended, suspended_reason, suspended_at,
  last_verified_at, verification_report, created_at, updated_at
FROM stage_apps
ON CONFLICT (id) DO UPDATE SET
  slug = EXCLUDED.slug,
  name = EXCLUDED.name,
  description = EXCLUDED.description,
  web_url = EXCLUDED.web_url,
  mcp_url = EXCLUDED.mcp_url,
  signing_secret = EXCLUDED.signing_secret,
  enabled = EXCLUDED.enabled,
  owner_id = EXCLUDED.owner_id,
  is_published = EXCLUDED.is_published,
  published_version = EXCLUDED.published_version,
  review_status = EXCLUDED.review_status,
  draft_name = EXCLUDED.draft_name,
  draft_description = EXCLUDED.draft_description,
  draft_web_url = EXCLUDED.draft_web_url,
  draft_mcp_url = EXCLUDED.draft_mcp_url,
  has_draft_changes = EXCLUDED.has_draft_changes,
  submission_revision = EXCLUDED.submission_revision,
  submission_note = EXCLUDED.submission_note,
  submitted_at = EXCLUDED.submitted_at,
  reviewed_by = EXCLUDED.reviewed_by,
  reviewed_at = EXCLUDED.reviewed_at,
  review_note = EXCLUDED.review_note,
  admin_suspended = EXCLUDED.admin_suspended,
  suspended_reason = EXCLUDED.suspended_reason,
  suspended_at = EXCLUDED.suspended_at,
  last_verified_at = EXCLUDED.last_verified_at,
  verification_report = EXCLUDED.verification_report,
  created_at = EXCLUDED.created_at,
  updated_at = EXCLUDED.updated_at
WHERE public.marketplace_apps.updated_at <= EXCLUDED.updated_at;

DELETE FROM public.workspace_marketplace_apps w
USING stage_apps sa, public.marketplace_apps m
WHERE w.app_id = sa.id
  AND m.id = sa.id
  AND m.updated_at <= sa.updated_at
  AND NOT EXISTS (
    SELECT 1 FROM stage_installs s
    WHERE s.workspace_id = w.workspace_id
      AND s.app_id = w.app_id
  );

INSERT INTO public.workspace_marketplace_apps (
  workspace_id, app_id, installed_by, review_preview, created_at
)
SELECT workspace_id, app_id, installed_by, review_preview, created_at
FROM stage_installs
ON CONFLICT (workspace_id, app_id) DO UPDATE SET
  installed_by = EXCLUDED.installed_by,
  review_preview = EXCLUDED.review_preview,
  created_at = EXCLUDED.created_at
WHERE NOT EXISTS (
  SELECT 1 FROM public.marketplace_apps m
  JOIN stage_apps sa ON sa.id = m.id
  WHERE m.id = EXCLUDED.app_id
    AND m.updated_at > sa.updated_at
);

INSERT INTO public.marketplace_app_reviews (
  id, app_id, revision, action, actor_id, note, web_url, mcp_url, snapshot, created_at
)
SELECT id, app_id, revision, action, actor_id, note, web_url, mcp_url, snapshot, created_at
FROM stage_reviews
ON CONFLICT (id) DO NOTHING;

DO $$
DECLARE
  missing_apps bigint;
  missing_installs bigint;
  extra_installs bigint;
  missing_reviews bigint;
BEGIN
  SELECT count(*) INTO missing_apps
  FROM stage_apps s
  WHERE NOT EXISTS (
    SELECT 1 FROM public.marketplace_apps m
    WHERE m.id = s.id
      AND m.slug = s.slug
      AND m.name = s.name
      AND m.description = s.description
      AND m.web_url = s.web_url
      AND m.mcp_url = s.mcp_url
      AND m.signing_secret = s.signing_secret
      AND m.enabled = s.enabled
      AND m.owner_id IS NOT DISTINCT FROM s.owner_id
      AND m.is_published = s.is_published
      AND m.published_version = s.published_version
      AND m.review_status = s.review_status
      AND m.draft_name = s.draft_name
      AND m.draft_description = s.draft_description
      AND m.draft_web_url = s.draft_web_url
      AND m.draft_mcp_url = s.draft_mcp_url
      AND m.has_draft_changes = s.has_draft_changes
      AND m.submission_revision = s.submission_revision
      AND m.submission_note = s.submission_note
      AND m.submitted_at IS NOT DISTINCT FROM s.submitted_at
      AND m.reviewed_by IS NOT DISTINCT FROM s.reviewed_by
      AND m.reviewed_at IS NOT DISTINCT FROM s.reviewed_at
      AND m.review_note = s.review_note
      AND m.admin_suspended = s.admin_suspended
      AND m.suspended_reason = s.suspended_reason
      AND m.suspended_at IS NOT DISTINCT FROM s.suspended_at
      AND m.last_verified_at IS NOT DISTINCT FROM s.last_verified_at
      AND m.verification_report IS NOT DISTINCT FROM s.verification_report
      AND m.created_at = s.created_at
      AND m.updated_at = s.updated_at
  );
  IF missing_apps > 0 THEN
    RAISE EXCEPTION 'marketplace_apps transfer verification failed: % row(s) missing or diverged', missing_apps;
  END IF;

  SELECT count(*) INTO missing_installs
  FROM stage_installs s
  WHERE NOT EXISTS (
    SELECT 1 FROM public.workspace_marketplace_apps w
    WHERE w.workspace_id = s.workspace_id
      AND w.app_id = s.app_id
      AND w.installed_by = s.installed_by
      AND w.review_preview = s.review_preview
      AND w.created_at = s.created_at
  );
  SELECT count(*) INTO extra_installs
  FROM public.workspace_marketplace_apps w
  JOIN stage_apps sa ON sa.id = w.app_id
  WHERE NOT EXISTS (
    SELECT 1 FROM stage_installs s
    WHERE s.workspace_id = w.workspace_id
      AND s.app_id = w.app_id
  );
  IF missing_installs > 0 OR extra_installs > 0 THEN
    RAISE EXCEPTION 'workspace_marketplace_apps transfer verification failed: % missing, % extra', missing_installs, extra_installs;
  END IF;

  SELECT count(*) INTO missing_reviews
  FROM stage_reviews s
  WHERE NOT EXISTS (
    SELECT 1 FROM public.marketplace_app_reviews r
    WHERE r.id = s.id
      AND r.app_id = s.app_id
      AND r.revision = s.revision
      AND r.action = s.action
      AND r.actor_id = s.actor_id
      AND r.note = s.note
      AND r.web_url = s.web_url
      AND r.mcp_url = s.mcp_url
      AND r.snapshot IS NOT DISTINCT FROM s.snapshot
      AND r.created_at = s.created_at
  );
  IF missing_reviews > 0 THEN
    RAISE EXCEPTION 'marketplace_app_reviews transfer verification failed: % row(s) missing or diverged', missing_reviews;
  END IF;
END
$$;

COMMIT;
SQL
  } | psql --username "${POSTGRES_SUPERUSER:-postgres}" --dbname "$marketplace_db" --set ON_ERROR_STOP=1 >/dev/null
)

"${psql_super[@]}" --dbname "$chat_db" --command "COMMENT ON TABLE public.marketplace_apps IS 'teamie.transferred_to_marketplace_v1';" >/dev/null
