CREATE TABLE IF NOT EXISTS platform_announcements (
  seq bigserial PRIMARY KEY,
  id text NOT NULL UNIQUE,
  title text NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 120),
  body text NOT NULL CHECK (char_length(btrim(body)) BETWEEN 1 AND 8000),
  author_id text NOT NULL,
  author_name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS platform_announcement_reads (
  user_id text PRIMARY KEY,
  read_seq bigint NOT NULL DEFAULT 0 CHECK (read_seq >= 0),
  updated_at timestamptz NOT NULL DEFAULT now()
);
