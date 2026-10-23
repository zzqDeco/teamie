ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS context_profile text NOT NULL DEFAULT 'teamie-context-v037';
ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS context_budget jsonb NOT NULL DEFAULT '{}';
ALTER TABLE runtime_session_acceptances ADD COLUMN IF NOT EXISTS run_id uuid REFERENCES runtime_runs(id);
UPDATE runtime_session_acceptances a SET run_id=r.id FROM runtime_runs r WHERE a.run_id IS NULL AND a.agent_id=r.agent_id AND (a.attempt_id=r.committed_attempt OR a.attempt_id=ANY(r.context_attempts));
UPDATE runtime_session_acceptances a SET run_id=e.run_id FROM (SELECT DISTINCT attempt_id,run_id FROM runtime_session_events) e WHERE a.run_id IS NULL AND a.attempt_id=e.attempt_id;
CREATE INDEX IF NOT EXISTS runtime_session_acceptances_run ON runtime_session_acceptances(agent_id,run_id,seq);
INSERT INTO agent_runtime_schema_migrations(version) VALUES(3) ON CONFLICT DO NOTHING;
CREATE OR REPLACE FUNCTION agent_runtime_check_writer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE(NULLIF(current_setting('teamie.runtime_writer_schema',true),''),'0')::integer < 3 THEN
  RAISE EXCEPTION 'unsupported runtime writer protocol' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS agent_runtime_writer_guard ON agent_runtime_schema_migrations;
CREATE TRIGGER agent_runtime_writer_guard BEFORE INSERT OR UPDATE OR DELETE
 ON agent_runtime_schema_migrations FOR EACH ROW EXECUTE FUNCTION agent_runtime_check_writer();
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
CREATE TABLE IF NOT EXISTS runtime_context_recoveries (
 run_id uuid NOT NULL REFERENCES runtime_runs(id),
 model_step integer NOT NULL CHECK(model_step > 0),
 context_profile text NOT NULL CHECK(context_profile = 'teamie-context-v038'),
 request_hash text NOT NULL,
 streaming boolean NOT NULL,
 error_code text NOT NULL CHECK(error_code IN ('context_overflow','request_too_large')),
 decision_version integer NOT NULL CHECK(decision_version = 1),
 decision_json jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(run_id,model_step)
);
