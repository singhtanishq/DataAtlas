-- 009: Dedicated application database role (defense-in-depth).
--
-- The Docker bootstrap superuser bypasses Row-Level Security by definition.
-- All application queries therefore run as dataatlas_app, a NOSUPERUSER,
-- NOBYPASSRLS role that is fully subject to the RLS policies created in
-- migration 008. The migration runner keeps running as the table owner.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dataatlas_app') THEN
        CREATE ROLE dataatlas_app LOGIN PASSWORD 'app-local-dev-only' NOSUPERUSER NOBYPASSRLS;
    END IF;
END
$$;

GRANT USAGE ON SCHEMA public TO dataatlas_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dataatlas_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dataatlas_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO dataatlas_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO dataatlas_app;
