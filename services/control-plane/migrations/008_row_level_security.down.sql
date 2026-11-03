DO $$
DECLARE
    t TEXT;
    all_tables TEXT[] := ARRAY[
        'organizations', 'memberships', 'teams', 'team_members',
        'data_sources', 'data_source_status_history', 'source_credentials',
        'ingestion_pipelines', 'ingestion_schedules', 'ingestion_runs',
        'ingestion_run_logs', 'connector_checkpoints',
        'data_assets', 'datasets', 'schema_versions', 'schema_columns', 'dataset_columns',
        'jobs', 'job_runs', 'job_inputs', 'job_outputs',
        'lineage_edges', 'column_lineage_edges',
        'profiling_runs', 'profile_snapshots', 'column_profiles',
        'quality_suites', 'quality_checks', 'quality_runs', 'quality_results', 'quality_incidents',
        'freshness_records',
        'business_glossaries', 'glossary_terms', 'glossary_term_relationships',
        'tags', 'ownership_assignments', 'classification_rules',
        'asset_classifications', 'column_classifications',
        'alert_rules', 'alerts', 'notifications', 'audit_logs', 'api_keys', 'webhooks'
    ];
BEGIN
    FOREACH t IN ARRAY all_tables LOOP
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_isolation', t);
        EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
    END LOOP;
    EXECUTE 'DROP POLICY IF EXISTS organizations_self ON organizations';
END
$$;
