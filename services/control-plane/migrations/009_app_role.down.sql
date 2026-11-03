-- 009 down: revoke the application role's privileges in this database.
--
-- Privilege grants are per-database, so revoking them here fully removes
-- application access to this database's data. The role itself (cluster
-- object) is intentionally left untouched: statements like ALTER ROLE ...
-- NOLOGIN or DROP ROLE act cluster-wide and would leak into every other
-- database sharing this PostgreSQL instance.

ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM dataatlas_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE USAGE, SELECT ON SEQUENCES FROM dataatlas_app;
REVOKE SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public FROM dataatlas_app;
REVOKE USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public FROM dataatlas_app;
REVOKE USAGE ON SCHEMA public FROM dataatlas_app;
