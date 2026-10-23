-- Exact v0.3.7 Runtime startup DDL from 8e7ba5720e396700e26a5a6966aa76d4f8da63a7.
-- schemaSQL

CREATE TABLE IF NOT EXISTS agent_runtime_schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS runtime_agents(agent_id uuid PRIMARY KEY, cursor numeric(30) NOT NULL DEFAULT 0, lease_owner text, lease_until timestamptz, fence bigint NOT NULL DEFAULT 0, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS runtime_inputs(agent_id uuid NOT NULL, event_id numeric(30) NOT NULL, payload jsonb NOT NULL, state text NOT NULL DEFAULT 'pending', run_id uuid, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(agent_id,event_id));
CREATE TABLE IF NOT EXISTS runtime_runs(id uuid PRIMARY KEY, agent_id uuid NOT NULL, input_key text NOT NULL, input_json jsonb NOT NULL, state text NOT NULL DEFAULT 'running', attempt_id uuid NOT NULL, committed_attempt uuid, attempts integer NOT NULL DEFAULT 1, next_attempt timestamptz NOT NULL DEFAULT now(), safe_error text NOT NULL DEFAULT '', started_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz, UNIQUE(agent_id,input_key));
CREATE INDEX IF NOT EXISTS runtime_inputs_pending ON runtime_inputs(agent_id,event_id) WHERE state IN ('pending','processing');
CREATE TABLE IF NOT EXISTS runtime_session_events(seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, session_id text NOT NULL, event_id text NOT NULL, kind text NOT NULL, run_id uuid NOT NULL REFERENCES runtime_runs(id), attempt_id uuid NOT NULL, payload bytea NOT NULL, payload_hash text NOT NULL, UNIQUE(session_id,event_id));
CREATE INDEX IF NOT EXISTS runtime_session_order ON runtime_session_events(session_id,seq);
CREATE TABLE IF NOT EXISTS runtime_checkpoints(checkpoint_id text PRIMARY KEY, payload bytea NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS runtime_model_calls(run_id uuid NOT NULL REFERENCES runtime_runs(id), step integer NOT NULL, request_hash text NOT NULL, response bytea NOT NULL, PRIMARY KEY(run_id,step));
CREATE TABLE IF NOT EXISTS runtime_tool_calls(run_id uuid NOT NULL REFERENCES runtime_runs(id), call_id text NOT NULL, name text NOT NULL, request_hash text NOT NULL, result bytea, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(run_id,call_id));
INSERT INTO agent_runtime_schema_migrations(version) VALUES(1) ON CONFLICT DO NOTHING;
-- schemaV2SQL

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
