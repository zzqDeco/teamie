package engine

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
