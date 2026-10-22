package engine

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
