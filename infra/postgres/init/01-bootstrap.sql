-- PostgreSQL bootstrap executed once on first container start.
-- Creates:
--   dataatlas_app      - non-superuser application role (subject to RLS)
--   dataatlas_scanner  - read-only demo role for connector demonstrations
\set ON_ERROR_STOP on

-- Application role: no superuser, no BYPASSRLS. All service queries run
-- through this role so Row-Level Security policies are always enforced.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dataatlas_app') THEN
        CREATE ROLE dataatlas_app LOGIN PASSWORD 'app-local-dev-only' NOSUPERUSER NOBYPASSRLS;
    END IF;
END
$$;

-- Read-only role for scanning the demo operational source database.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dataatlas_scanner') THEN
        CREATE ROLE dataatlas_scanner LOGIN PASSWORD 'scanner-demo-local-only' NOSUPERUSER NOBYPASSRLS;
    END IF;
END
$$;

-- Grants for the application role on the metadata schema.
GRANT USAGE ON SCHEMA public TO dataatlas_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dataatlas_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dataatlas_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO dataatlas_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO dataatlas_app;

-- The demo source databases are created in later milestones along with
-- their schemas; the scanner role receives grants there.
