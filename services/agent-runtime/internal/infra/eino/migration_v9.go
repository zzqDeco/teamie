package eino

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
