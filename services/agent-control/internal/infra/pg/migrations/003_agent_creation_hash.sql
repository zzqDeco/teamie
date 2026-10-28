-- Legacy Agents keep an empty hash; never infer an original request from a mutable profile.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS creation_hash text NOT NULL DEFAULT '';
