package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const RuntimeSchemaVersion = 12
const RuntimeWriterSchemaSetting = "teamie.runtime_writer_schema"

var ErrUnsupportedRuntimeSchema = errors.New("unsupported runtime schema")

const schemaSQL = `
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
INSERT INTO agent_runtime_schema_migrations(version) VALUES(1) ON CONFLICT DO NOTHING;`

// Forward-only additions: v1 events, model tapes, tool receipts and inbox rows
// remain intact. Internal execution metadata is never returned by owner APIs.
const schemaV2SQL = `
ALTER TABLE runtime_agents ADD COLUMN IF NOT EXISTS state text NOT NULL DEFAULT 'idle';
ALTER TABLE runtime_agents ADD COLUMN IF NOT EXISTS last_error text NOT NULL DEFAULT '';
ALTER TABLE runtime_agents ADD COLUMN IF NOT EXISTS last_active_at timestamptz;
-- Only a first v1 upgrade has no delivery sequence. Identity ADD COLUMN would
-- backfill in heap order, which can differ from the previous event_id order.
-- Existing v2/v3 delivery values, including task notifications, are immutable.
DO $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='runtime_inputs'::regclass AND attname='delivery_seq' AND NOT attisdropped) THEN
  ALTER TABLE runtime_inputs ADD COLUMN delivery_seq bigint;
  WITH ordered AS (SELECT agent_id,event_id,row_number() OVER(ORDER BY agent_id,event_id) AS seq FROM runtime_inputs)
  UPDATE runtime_inputs i SET delivery_seq=o.seq FROM ordered o WHERE i.agent_id=o.agent_id AND i.event_id=o.event_id;
  ALTER TABLE runtime_inputs ALTER COLUMN delivery_seq SET NOT NULL;
  ALTER TABLE runtime_inputs ALTER COLUMN delivery_seq ADD GENERATED ALWAYS AS IDENTITY;
  PERFORM setval(pg_get_serial_sequence('runtime_inputs','delivery_seq')::regclass,COALESCE((SELECT max(delivery_seq) FROM runtime_inputs),1),EXISTS(SELECT 1 FROM runtime_inputs));
 END IF;
END;
$$;
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

// These are private Runtime records. Neither Chat nor the browser reads this table.
const contextArchiveSchemaSQL = `
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
`

const contextRecoverySchemaSQL = `
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
);`

// Budget version 2 is durable Runtime data. Fence binaries that cannot read it
// without rewriting any existing Run, checkpoint or model/tool receipt.
const schemaV4SQL = `
CREATE OR REPLACE FUNCTION agent_runtime_check_writer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE(NULLIF(current_setting('teamie.runtime_writer_schema',true),''),'0')::integer < 4 THEN
  RAISE EXCEPTION 'unsupported runtime writer protocol' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
INSERT INTO agent_runtime_schema_migrations(version) VALUES(4) ON CONFLICT DO NOTHING;
`

// Current-input retention is part of the frozen model-input policy. A reader
// that only understands earlier budget snapshots must not start after it.
const schemaV5SQL = `
CREATE OR REPLACE FUNCTION agent_runtime_check_writer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE(NULLIF(current_setting('teamie.runtime_writer_schema',true),''),'0')::integer < 5 THEN
  RAISE EXCEPTION 'unsupported runtime writer protocol' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
INSERT INTO agent_runtime_schema_migrations(version) VALUES(5) ON CONFLICT DO NOTHING;
`

// Historical-fragment coalescing changes the frozen model-input policy. Old
// writers must remain fenced while all prior snapshot readers stay supported.
const schemaV6SQL = `
CREATE OR REPLACE FUNCTION agent_runtime_check_writer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE(NULLIF(current_setting('teamie.runtime_writer_schema',true),''),'0')::integer < 6 THEN
  RAISE EXCEPTION 'unsupported runtime writer protocol' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
INSERT INTO agent_runtime_schema_migrations(version) VALUES(6) ON CONFLICT DO NOTHING;
`

// Task-notification reduction changes new frozen model inputs. Preserve all
// historical policies and fence writers that cannot read the new snapshot.
const schemaV7SQL = `
CREATE OR REPLACE FUNCTION agent_runtime_check_writer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE(NULLIF(current_setting('teamie.runtime_writer_schema',true),''),'0')::integer < 7 THEN
  RAISE EXCEPTION 'unsupported runtime writer protocol' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
INSERT INTO agent_runtime_schema_migrations(version) VALUES(7) ON CONFLICT DO NOTHING;
`

// Long-context capacities affect newly frozen model inputs. Older policies and
// receipts remain unchanged; old writers cannot consume the new snapshots.
const schemaV8SQL = `
CREATE OR REPLACE FUNCTION agent_runtime_check_writer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE(NULLIF(current_setting('teamie.runtime_writer_schema',true),''),'0')::integer < 8 THEN
  RAISE EXCEPTION 'unsupported runtime writer protocol' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
INSERT INTO agent_runtime_schema_migrations(version) VALUES(8) ON CONFLICT DO NOTHING;
`

// The event-driven guidance selection is part of a frozen Run snapshot. Fence
// writers whose strict snapshot reader cannot understand the new field, while
// preserving every previous Run, receipt and native checkpoint unchanged.
const schemaV9SQL = `
CREATE OR REPLACE FUNCTION agent_runtime_check_writer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE(NULLIF(current_setting('teamie.runtime_writer_schema',true),''),'0')::integer < 9 THEN
  RAISE EXCEPTION 'unsupported runtime writer protocol' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
INSERT INTO agent_runtime_schema_migrations(version) VALUES(9) ON CONFLICT DO NOTHING;
`

// Context budget snapshot version 7 introduces visible-byte division by 4 and
// request-bound PromptTokens delta calibration. Fence writers whose strict
// snapshot reader cannot understand version 7, while preserving every previous
// Run, receipt and native checkpoint unchanged.
const schemaV10SQL = `
CREATE OR REPLACE FUNCTION agent_runtime_check_writer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE(NULLIF(current_setting('teamie.runtime_writer_schema',true),''),'0')::integer < 10 THEN
  RAISE EXCEPTION 'unsupported runtime writer protocol' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
INSERT INTO agent_runtime_schema_migrations(version) VALUES(10) ON CONFLICT DO NOTHING;
`

// Frozen context budget snapshots may now include the optional webSearch boolean
// when agents.web.v1 is enabled at Run admission. Fence older runtime writers
// whose strict DisallowUnknownFields snapshot parser cannot read webSearch,
// while preserving every previous Run, receipt and native checkpoint unchanged.
const schemaV11SQL = `
CREATE OR REPLACE FUNCTION agent_runtime_check_writer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE(NULLIF(current_setting('teamie.runtime_writer_schema',true),''),'0')::integer < 11 THEN
  RAISE EXCEPTION 'unsupported runtime writer protocol' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
INSERT INTO agent_runtime_schema_migrations(version) VALUES(11) ON CONFLICT DO NOTHING;
`

// Frozen context budget snapshots may now include the optional scheduledWakeups
// boolean when agents.schedule.v1 is enabled at Run admission, and agent-owned
// durable schedules are stored in runtime_schedules. Fence older runtime
// writers whose strict DisallowUnknownFields snapshot parser cannot read
// scheduledWakeups, while preserving every previous Run, receipt and native
// checkpoint unchanged.
const schemaV12SQL = `
CREATE TABLE IF NOT EXISTS runtime_schedules(
 id text PRIMARY KEY,
 agent_id uuid NOT NULL,
 created_run_id uuid NOT NULL REFERENCES runtime_runs(id),
 kind text NOT NULL,
 interval_seconds bigint,
 cron_expr text NOT NULL DEFAULT '',
 timezone text NOT NULL DEFAULT 'UTC',
 prompt text NOT NULL,
 status text NOT NULL DEFAULT 'active',
 max_runs integer,
 run_count integer NOT NULL DEFAULT 0,
 next_run_at timestamptz NOT NULL,
 last_run_at timestamptz,
 source_refs jsonb NOT NULL DEFAULT '[]'::jsonb,
 source_overflow boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS runtime_schedules_agent_due ON runtime_schedules(agent_id, next_run_at) WHERE status = 'active';
CREATE OR REPLACE FUNCTION agent_runtime_check_writer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE(NULLIF(current_setting('teamie.runtime_writer_schema',true),''),'0')::integer < 12 THEN
  RAISE EXCEPTION 'unsupported runtime writer protocol' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
INSERT INTO agent_runtime_schema_migrations(version) VALUES(12) ON CONFLICT DO NOTHING;
`

const (
	SchemaSQL                = schemaSQL
	SchemaV2SQL              = schemaV2SQL
	SchemaV3SQL              = schemaV3SQL
	ContextArchiveSchemaSQL  = contextArchiveSchemaSQL
	ContextRecoverySchemaSQL = contextRecoverySchemaSQL
	SchemaV4SQL              = schemaV4SQL
	SchemaV5SQL              = schemaV5SQL
	SchemaV6SQL              = schemaV6SQL
	SchemaV7SQL              = schemaV7SQL
	SchemaV8SQL              = schemaV8SQL
	SchemaV9SQL              = schemaV9SQL
	SchemaV10SQL             = schemaV10SQL
	SchemaV11SQL             = schemaV11SQL
	SchemaV12SQL             = schemaV12SQL
)

// ValidateRuntimeSchema validates the schema ledger before any DDL, including
// replacement of the writer guard. The ledger is written atomically by Open, so
// an existing empty or gapped ledger is an incomplete schema rather than a fresh
// database to silently repair.
func ValidateRuntimeSchema(ctx context.Context, tx pgx.Tx) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('agent_runtime_schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT version FROM agent_runtime_schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	defer rows.Close()
	want := 1
	for rows.Next() {
		var version int
		if err = rows.Scan(&version); err != nil {
			return err
		}
		if version != want || version > RuntimeSchemaVersion {
			return fmt.Errorf("%w: found version %d, expected %d through %d", ErrUnsupportedRuntimeSchema, version, want, RuntimeSchemaVersion)
		}
		want++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if want == 1 {
		return fmt.Errorf("%w: existing ledger is empty", ErrUnsupportedRuntimeSchema)
	}
	return nil
}

// OpenRepository opens the Agent Runtime PostgreSQL pool, validates and applies
// forward schema migrations under advisory lock 704205, and returns a Repository.
func OpenRepository(ctx context.Context, url string) (*Repository, error) {
	db, err := OpenPool(ctx, url, RuntimeWriterSchemaSetting, RuntimeSchemaVersion)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(704205)"); err == nil {
		err = ValidateRuntimeSchema(ctx, tx)
	}
	if err == nil {
		_, err = tx.Exec(ctx, schemaSQL+schemaV2SQL+schemaV3SQL+contextArchiveSchemaSQL+contextRecoverySchemaSQL+schemaV4SQL+schemaV5SQL+schemaV6SQL+schemaV7SQL+schemaV8SQL+schemaV9SQL+schemaV10SQL+schemaV11SQL+schemaV12SQL)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		db.Close()
		return nil, err
	}
	return NewFromPool(db), nil
}
