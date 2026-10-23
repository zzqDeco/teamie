CREATE TABLE IF NOT EXISTS runtime_context_artifacts(
 seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 agent_id uuid NOT NULL,
 run_id uuid NOT NULL REFERENCES runtime_runs(id),
 attempt_id uuid NOT NULL,
 artifact_key text NOT NULL,
 kind text NOT NULL,
 tool_call_id text NOT NULL DEFAULT '',
 tool_name text NOT NULL DEFAULT '',
 content bytea NOT NULL,
 content_hash text NOT NULL,
 sources jsonb NOT NULL,
 source_artifact_refs jsonb NOT NULL DEFAULT '[]',
 source_event_seq bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(agent_id,run_id,artifact_key)
);
ALTER TABLE runtime_context_artifacts ADD COLUMN IF NOT EXISTS source_artifact_refs jsonb NOT NULL DEFAULT '[]';
CREATE INDEX IF NOT EXISTS runtime_context_artifacts_agent_order ON runtime_context_artifacts(agent_id,seq);
CREATE INDEX IF NOT EXISTS runtime_context_artifacts_agent_key ON runtime_context_artifacts(agent_id,artifact_key);
