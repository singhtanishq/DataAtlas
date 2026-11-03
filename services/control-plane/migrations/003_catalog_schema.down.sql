ALTER TABLE datasets DROP CONSTRAINT IF EXISTS datasets_current_version_fk;
DROP TABLE IF EXISTS dataset_versions;
DROP TABLE IF EXISTS dataset_columns;
DROP TABLE IF EXISTS schema_columns;
DROP TABLE IF EXISTS schema_versions;
DROP TABLE IF EXISTS datasets;
DROP TABLE IF EXISTS data_assets;
