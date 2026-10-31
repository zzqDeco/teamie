-- Freeze conversational-style prompt/tool admission in schema 14.
-- Older readers reject the new snapshot field; stop them before they can write.
CREATE OR REPLACE FUNCTION agent_runtime_check_writer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF COALESCE(NULLIF(current_setting('teamie.runtime_writer_schema',true),''),'0')::integer < 14 THEN
  RAISE EXCEPTION 'unsupported runtime writer protocol' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
INSERT INTO agent_runtime_schema_migrations(version) VALUES(14) ON CONFLICT DO NOTHING;

-- Fence already-open old writers on all Runtime-owned durable tables.
DO $$
DECLARE owned_table record;
BEGIN
 FOR owned_table IN
  SELECT c.oid::regclass AS relation FROM pg_class c
  WHERE c.relnamespace=(SELECT relnamespace FROM pg_class WHERE oid='agent_runtime_schema_migrations'::regclass)
    AND c.relkind='r' AND starts_with(c.relname, 'runtime_')
 LOOP
  EXECUTE format('DROP TRIGGER IF EXISTS agent_runtime_writer_guard ON %s', owned_table.relation);
  EXECUTE format('CREATE TRIGGER agent_runtime_writer_guard BEFORE INSERT OR UPDATE OR DELETE ON %s FOR EACH ROW EXECUTE FUNCTION agent_runtime_check_writer()', owned_table.relation);
 END LOOP;
END;
$$;
