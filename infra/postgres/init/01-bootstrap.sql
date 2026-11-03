-- PostgreSQL bootstrap executed once on first container start.
-- Creates the read-only demo scanner role used by demo ingestion connectors
-- (never the application admin account).
\set ON_ERROR_STOP on

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'dataatlas_scanner') THEN
        CREATE ROLE dataatlas_scanner LOGIN PASSWORD 'scanner-demo-local-only';
    END IF;
END
$$;

-- The demo source databases are created in later milestones along with
-- their schemas; the scanner role receives grants there.
