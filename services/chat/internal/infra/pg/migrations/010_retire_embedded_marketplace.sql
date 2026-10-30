-- Fail closed if legacy embedded Marketplace tables still contain rows that
-- have not been transferred and verified by deploy/postgres/02-migrate-chat-marketplace.sh.
DO $$
BEGIN
  IF to_regclass('marketplace_apps') IS NOT NULL
     AND EXISTS (SELECT 1 FROM marketplace_apps)
     AND COALESCE(obj_description(to_regclass('marketplace_apps'), 'pg_class'), '') <> 'teamie.transferred_to_marketplace_v1' THEN
    RAISE EXCEPTION 'Refusing to drop non-empty embedded marketplace tables before deploy/postgres/02-migrate-chat-marketplace.sh verifies transfer to teamie_marketplace';
  END IF;
END
$$;

DROP TABLE IF EXISTS workspace_marketplace_apps;
DROP TABLE IF EXISTS marketplace_app_reviews;
DROP TABLE IF EXISTS marketplace_apps;
