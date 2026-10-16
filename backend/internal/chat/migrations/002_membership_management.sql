ALTER TABLE workspace_members ADD COLUMN granted_by text;
ALTER TABLE conversations ADD COLUMN created_by text;
CREATE TABLE principal_membership_revisions(principal_id text PRIMARY KEY,revision bigint NOT NULL);
