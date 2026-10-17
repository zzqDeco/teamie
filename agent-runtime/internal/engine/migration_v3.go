package engine

// Existing Runs keep their exact legacy model/tool policy. New policies are
// immutable Run data, not process defaults applied retroactively on restart.
const schemaV3SQL = `
ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS context_profile text NOT NULL DEFAULT 'teamie-context-v037';
ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS context_budget jsonb NOT NULL DEFAULT '{}';
INSERT INTO agent_runtime_schema_migrations(version) VALUES(3) ON CONFLICT DO NOTHING;
`
