CREATE TABLE IF NOT EXISTS gateway_schema_migrations(version INTEGER PRIMARY KEY,applied_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS gateway_config(id BOOLEAN PRIMARY KEY DEFAULT true CHECK(id),base_url TEXT NOT NULL DEFAULT '',model TEXT NOT NULL DEFAULT '',encrypted_key BYTEA,enabled BOOLEAN NOT NULL DEFAULT false,updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
INSERT INTO gateway_config(id)VALUES(true)ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS gateway_usage(id BIGSERIAL PRIMARY KEY,owner_id TEXT NOT NULL,agent_id TEXT NOT NULL,session_id TEXT NOT NULL,run_id TEXT NOT NULL,status TEXT NOT NULL,prompt_tokens BIGINT NOT NULL,completion_tokens BIGINT NOT NULL,latency_ms BIGINT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS gateway_usage_created ON gateway_usage(created_at);
INSERT INTO gateway_schema_migrations(version)VALUES(1)ON CONFLICT DO NOTHING;
ALTER TABLE gateway_config ADD COLUMN IF NOT EXISTS context_window_tokens BIGINT NOT NULL DEFAULT 16384 CHECK(context_window_tokens BETWEEN 4096 AND 2000000);
INSERT INTO gateway_schema_migrations(version)VALUES(2)ON CONFLICT DO NOTHING;
ALTER TABLE gateway_config ADD COLUMN IF NOT EXISTS web_search_encrypted_key BYTEA, ADD COLUMN IF NOT EXISTS web_search_enabled BOOLEAN NOT NULL DEFAULT false;
INSERT INTO gateway_schema_migrations(version)VALUES(3)ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS gateway_models(id TEXT PRIMARY KEY,display_name TEXT NOT NULL DEFAULT '',description TEXT NOT NULL DEFAULT '',base_url TEXT NOT NULL DEFAULT '',model TEXT NOT NULL DEFAULT '',encrypted_key BYTEA,context_window_tokens BIGINT NOT NULL DEFAULT 16384 CHECK(context_window_tokens BETWEEN 4096 AND 2000000),enabled BOOLEAN NOT NULL DEFAULT true,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
ALTER TABLE gateway_config ADD COLUMN IF NOT EXISTS default_model_id TEXT NOT NULL DEFAULT 'default';
DO $$ BEGIN
IF NOT EXISTS (SELECT 1 FROM gateway_schema_migrations WHERE version=4) THEN
INSERT INTO gateway_models(id,display_name,description,base_url,model,encrypted_key,context_window_tokens,enabled,created_at,updated_at)SELECT 'default',model,'',base_url,model,encrypted_key,context_window_tokens,enabled,updated_at,updated_at FROM gateway_config WHERE id AND (base_url<>'' OR model<>'' OR encrypted_key IS NOT NULL) ON CONFLICT(id)DO NOTHING;
END IF;
END $$;
ALTER TABLE gateway_usage ADD COLUMN IF NOT EXISTS model_id TEXT NOT NULL DEFAULT '';
INSERT INTO gateway_schema_migrations(version)VALUES(4)ON CONFLICT DO NOTHING;

-- Split durable settings once. The legacy singleton is retained as an immutable
-- migration source; live code never synchronizes a resolved model back into it.
CREATE TABLE IF NOT EXISTS gateway_model_routing(id BOOLEAN PRIMARY KEY DEFAULT true CHECK(id),default_model_id TEXT NOT NULL DEFAULT '',updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS gateway_web_search_config(id BOOLEAN PRIMARY KEY DEFAULT true CHECK(id),encrypted_key BYTEA,enabled BOOLEAN NOT NULL DEFAULT false,updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
DO $$ BEGIN
IF NOT EXISTS (SELECT 1 FROM gateway_schema_migrations WHERE version=5) THEN
    INSERT INTO gateway_model_routing(id,default_model_id,updated_at) SELECT id,default_model_id,updated_at FROM gateway_config ON CONFLICT(id) DO NOTHING;
    INSERT INTO gateway_web_search_config(id,encrypted_key,enabled,updated_at) SELECT id,web_search_encrypted_key,web_search_enabled,updated_at FROM gateway_config ON CONFLICT(id) DO NOTHING;
    INSERT INTO gateway_schema_migrations(version) VALUES(5);
END IF;
END $$;

-- Split provider connections from model catalog once (Schema 6).
-- Existing models migrate 1-to-1 into gateway_providers without merging by base_url,
-- preserving exact ciphertexts, model IDs, enabled states and default_model_id.
CREATE TABLE IF NOT EXISTS gateway_providers(id TEXT PRIMARY KEY,display_name TEXT NOT NULL DEFAULT '',base_url TEXT NOT NULL DEFAULT '',encrypted_key BYTEA,enabled BOOLEAN NOT NULL DEFAULT true,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
DO $$ BEGIN
IF NOT EXISTS (SELECT 1 FROM gateway_schema_migrations WHERE version=6) THEN
    INSERT INTO gateway_providers(id,display_name,base_url,encrypted_key,enabled,created_at,updated_at)
    SELECT id,
           CASE WHEN btrim(display_name)<>'' THEN btrim(display_name) ELSE id END,
           base_url,
           encrypted_key,
           true,
           created_at,
           updated_at
    FROM gateway_models
    ON CONFLICT(id) DO NOTHING;

    ALTER TABLE gateway_models ADD COLUMN IF NOT EXISTS provider_id TEXT;
    UPDATE gateway_models SET provider_id=id WHERE provider_id IS NULL;
    ALTER TABLE gateway_models ALTER COLUMN provider_id SET NOT NULL;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname='gateway_models_provider_id_fkey'
          AND conrelid='gateway_models'::regclass
    ) THEN
        ALTER TABLE gateway_models
            ADD CONSTRAINT gateway_models_provider_id_fkey
            FOREIGN KEY (provider_id) REFERENCES gateway_providers(id) ON DELETE RESTRICT;
    END IF;
    CREATE INDEX IF NOT EXISTS gateway_models_provider_id ON gateway_models(provider_id);
    ALTER TABLE gateway_models DROP COLUMN IF EXISTS base_url, DROP COLUMN IF EXISTS encrypted_key;
    INSERT INTO gateway_schema_migrations(version) VALUES(6);
END IF;
END $$;
