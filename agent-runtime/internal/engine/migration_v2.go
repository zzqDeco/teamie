package engine

// Forward-only additions: v1 events, model tapes, tool receipts and inbox rows
// remain intact. Internal execution metadata is never returned by owner APIs.
const schemaV2SQL = `
ALTER TABLE runtime_agents ADD COLUMN IF NOT EXISTS state text NOT NULL DEFAULT 'idle';
ALTER TABLE runtime_agents ADD COLUMN IF NOT EXISTS last_error text NOT NULL DEFAULT '';
ALTER TABLE runtime_agents ADD COLUMN IF NOT EXISTS last_active_at timestamptz;
ALTER TABLE runtime_inputs ADD COLUMN IF NOT EXISTS delivery_seq bigint GENERATED ALWAYS AS IDENTITY;
ALTER TABLE runtime_inputs ADD COLUMN IF NOT EXISTS notification_id text;
CREATE UNIQUE INDEX IF NOT EXISTS runtime_inputs_notification ON runtime_inputs(agent_id,notification_id) WHERE notification_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS runtime_session_acceptances(seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,agent_id uuid NOT NULL,attempt_id uuid NOT NULL UNIQUE);
CREATE INDEX IF NOT EXISTS runtime_session_acceptances_agent ON runtime_session_acceptances(agent_id,seq);
INSERT INTO runtime_session_acceptances(agent_id,attempt_id) SELECT agent_id,committed_attempt FROM runtime_runs WHERE committed_attempt IS NOT NULL ORDER BY started_at ON CONFLICT DO NOTHING;
ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS acceptance_head bigint;
UPDATE runtime_runs r SET acceptance_head=COALESCE((SELECT max(a.seq) FROM runtime_session_acceptances a JOIN runtime_runs previous ON previous.committed_attempt=a.attempt_id WHERE a.agent_id=r.agent_id AND previous.started_at<r.started_at),0) WHERE acceptance_head IS NULL;
ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS context_head bigint;
UPDATE runtime_runs r SET context_head=COALESCE((SELECT min(seq)-1 FROM runtime_session_events WHERE run_id=r.id),0) WHERE context_head IS NULL;
ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 0;
ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS generation bigint NOT NULL DEFAULT 0;
ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS context_attempts uuid[] NOT NULL DEFAULT '{}';
ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS cancel_requested boolean NOT NULL DEFAULT false;
ALTER TABLE runtime_runs ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE runtime_checkpoints ADD COLUMN IF NOT EXISTS metadata jsonb;
CREATE TABLE IF NOT EXISTS runtime_tool_observations(run_id uuid NOT NULL REFERENCES runtime_runs(id),call_id text NOT NULL,observation_key text NOT NULL,response bytea NOT NULL,PRIMARY KEY(run_id,call_id,observation_key));
CREATE TABLE IF NOT EXISTS runtime_tasks(id text PRIMARY KEY,agent_id uuid NOT NULL,run_id uuid,snapshot jsonb NOT NULL,lease_until timestamptz,created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS runtime_tasks_agent ON runtime_tasks(agent_id,id);
CREATE TABLE IF NOT EXISTS runtime_task_events(seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,task_id text NOT NULL REFERENCES runtime_tasks(id),event_id text NOT NULL,payload bytea NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(task_id,event_id));
CREATE TABLE IF NOT EXISTS runtime_task_notifications(seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,id text NOT NULL UNIQUE,agent_id uuid NOT NULL,task_id text NOT NULL REFERENCES runtime_tasks(id),snapshot jsonb NOT NULL,receipt bytea,lease_until timestamptz,acked boolean NOT NULL DEFAULT false);
CREATE TABLE IF NOT EXISTS runtime_custom_notifications(task_id text NOT NULL REFERENCES runtime_tasks(id),event_id text NOT NULL,kind text NOT NULL,data bytea NOT NULL,PRIMARY KEY(task_id,event_id));
CREATE TABLE IF NOT EXISTS runtime_public_events(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,agent_id uuid NOT NULL,type text NOT NULL,run_id uuid,task_id text,tool_name text,status text,error_code text,created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS runtime_public_events_agent ON runtime_public_events(agent_id,id);
CREATE SEQUENCE IF NOT EXISTS runtime_notification_input_seq;
INSERT INTO agent_runtime_schema_migrations(version) VALUES(2) ON CONFLICT DO NOTHING;
`
