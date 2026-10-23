package eino

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
