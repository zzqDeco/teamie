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
`
