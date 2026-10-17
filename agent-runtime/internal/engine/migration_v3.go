package engine

// Existing Runs keep their exact legacy model/tool policy. New policies are
// immutable Run data, not process defaults applied retroactively on restart.
const schemaV3SQL = `
ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS context_profile text NOT NULL DEFAULT 'teamie-context-v037';
ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS context_budget jsonb NOT NULL DEFAULT '{}';
ALTER TABLE runtime_session_acceptances ADD COLUMN IF NOT EXISTS run_id uuid REFERENCES runtime_runs(id);
UPDATE runtime_session_acceptances a SET run_id=r.id FROM runtime_runs r WHERE a.run_id IS NULL AND a.agent_id=r.agent_id AND (a.attempt_id=r.committed_attempt OR a.attempt_id=ANY(r.context_attempts));
UPDATE runtime_session_acceptances a SET run_id=e.run_id FROM (SELECT DISTINCT attempt_id,run_id FROM runtime_session_events) e WHERE a.run_id IS NULL AND a.attempt_id=e.attempt_id;
CREATE INDEX IF NOT EXISTS runtime_session_acceptances_run ON runtime_session_acceptances(agent_id,run_id,seq);
INSERT INTO agent_runtime_schema_migrations(version) VALUES(3) ON CONFLICT DO NOTHING;

-- A v037 Open always inserts version 1, even when it already exists. BEFORE
-- INSERT runs before ON CONFLICT, so this rejects the old binary's complete
-- startup transaction without requiring a change to that published image.
-- The connection marker is a compatibility protocol, not authorization
-- against an operator who already controls the owning database role.
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
`
