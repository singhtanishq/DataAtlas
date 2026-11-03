-- 008: Row-Level Security policies (defense-in-depth, Part 10.5).
--
-- Application-level authorization is the primary control; these policies
-- ensure that even a bug in a service query cannot return another
-- organization's rows. Every request transaction sets:
--   SET LOCAL app.current_organization_id = '<uuid>'
-- Queries that do not set it fail closed (return no rows).

DO $$
DECLARE
    t TEXT;
    simple_tables TEXT[] := ARRAY[
        'memberships', 'teams',
        'data_sources', 'data_source_status_history', 'source_credentials',
        'ingestion_pipelines', 'ingestion_schedules', 'ingestion_runs',
        'ingestion_run_logs', 'connector_checkpoints',
        'data_assets', 'datasets', 'schema_versions', 'schema_columns', 'dataset_columns',
        'jobs', 'job_runs',
        'lineage_edges', 'column_lineage_edges',
        'profiling_runs', 'profile_snapshots', 'column_profiles',
        'quality_suites', 'quality_checks', 'quality_runs', 'quality_results', 'quality_incidents',
        'freshness_records',
        'business_glossaries', 'glossary_terms', 'glossary_term_relationships',
        'tags', 'ownership_assignments', 'classification_rules',
        'asset_classifications', 'column_classifications',
        'alert_rules', 'alerts', 'notifications', 'audit_logs', 'api_keys', 'webhooks'
    ];
    join_tables TEXT[] := ARRAY['team_members', 'job_inputs', 'job_outputs'];
BEGIN
    -- organizations is self-referencing: policy compares its own id.
    EXECUTE 'ALTER TABLE organizations ENABLE ROW LEVEL SECURITY';
    EXECUTE 'ALTER TABLE organizations FORCE ROW LEVEL SECURITY';
    EXECUTE $f$
        CREATE POLICY organizations_isolation ON organizations
        USING (id = NULLIF(current_setting('app.current_organization_id', true), '')::uuid)
        WITH CHECK (id = NULLIF(current_setting('app.current_organization_id', true), '')::uuid)
    $f$;

    FOREACH t IN ARRAY simple_tables LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            CREATE POLICY %I_isolation ON %I
            USING (organization_id = NULLIF(current_setting('app.current_organization_id', true), '')::uuid)
            WITH CHECK (organization_id = NULLIF(current_setting('app.current_organization_id', true), '')::uuid)
        $f$, t, t, t);
    END LOOP;

    -- Tables keyed through a parent row: derive the organization via join.
    FOREACH t IN ARRAY join_tables LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        IF t = 'team_members' THEN
            EXECUTE $f$
                CREATE POLICY team_members_isolation ON team_members
                USING (EXISTS (
                    SELECT 1 FROM teams tm
                    WHERE tm.id = team_members.team_id
                      AND tm.organization_id = NULLIF(current_setting('app.current_organization_id', true), '')::uuid
                ))
            $f$;
        ELSE
            EXECUTE format($f$
                CREATE POLICY %I_isolation ON %I
                USING (EXISTS (
                    SELECT 1 FROM jobs j
                    WHERE j.id = %I.job_id
                      AND j.organization_id = NULLIF(current_setting('app.current_organization_id', true), '')::uuid
                ))
            $f$, t, t, t);
        END IF;
    END LOOP;
END
$$;
