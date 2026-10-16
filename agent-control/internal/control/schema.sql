CREATE TABLE agents (
 id text PRIMARY KEY,owner_id text NOT NULL,name text NOT NULL,description text NOT NULL DEFAULT '',
 workspace_ids text[] NOT NULL DEFAULT '{}',enabled boolean NOT NULL DEFAULT false,
 revision bigint NOT NULL DEFAULT 1,membership_revision bigint NOT NULL DEFAULT 1,
 applied_revision bigint NOT NULL DEFAULT 0,applied_membership_revision bigint NOT NULL DEFAULT 0,
 policy_enabled boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),deleted_at timestamptz,
 runtime_state text NOT NULL DEFAULT 'provisioning',last_active_at timestamptz,last_error text NOT NULL DEFAULT ''
);
CREATE INDEX agents_owner ON agents(owner_id,created_at);
CREATE INDEX agents_workspaces ON agents USING gin(workspace_ids);
