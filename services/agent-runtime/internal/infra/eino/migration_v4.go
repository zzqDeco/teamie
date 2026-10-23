package eino

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
