-- 005: Profiling and data quality entities.

-- One row per profiler execution over a dataset.
CREATE TABLE profiling_runs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dataset_id          UUID NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    ingestion_run_id    UUID REFERENCES ingestion_runs(id) ON DELETE SET NULL,
    status              TEXT NOT NULL DEFAULT 'RUNNING',
    scan_strategy       TEXT NOT NULL DEFAULT 'SAMPLE',
    sample_rows         BIGINT,
    scanned_rows        BIGINT,
    duration_ms         BIGINT,
    error_message       TEXT,
    started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at         TIMESTAMPTZ,
    CONSTRAINT profiling_runs_status_allowed CHECK (status IN ('RUNNING', 'SUCCEEDED', 'FAILED')),
    CONSTRAINT profiling_runs_strategy_allowed CHECK (scan_strategy IN (
        'FULL_SCAN', 'SAMPLE', 'APPROXIMATE', 'INFERRED'
    ))
);

CREATE INDEX idx_profiling_runs_dataset ON profiling_runs (dataset_id, started_at DESC);

-- Dataset-level aggregate snapshot.
CREATE TABLE profile_snapshots (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profiling_run_id    UUID NOT NULL REFERENCES profiling_runs(id) ON DELETE CASCADE,
    dataset_id          UUID NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    row_count           BIGINT,
    column_count        INTEGER,
    metrics             JSONB NOT NULL DEFAULT '{}',
    metric_method       JSONB NOT NULL DEFAULT '{}', -- per-metric provenance
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_profile_snapshots_dataset ON profile_snapshots (dataset_id, created_at DESC);

-- Per-column profile metrics. value_type distinguishes exact, approximate,
-- sample-derived and inferred measurements (Part 14.2).
CREATE TABLE column_profiles (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    profiling_run_id    UUID NOT NULL REFERENCES profiling_runs(id) ON DELETE CASCADE,
    dataset_column_id   UUID NOT NULL REFERENCES dataset_columns(id) ON DELETE CASCADE,
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    metric_name         TEXT NOT NULL,
    metric_value        DOUBLE PRECISION,
    metric_value_text   TEXT,
    value_type          TEXT NOT NULL DEFAULT 'EXACT',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT column_profiles_value_type_allowed CHECK (value_type IN (
        'EXACT', 'APPROXIMATE', 'SAMPLE', 'INFERRED'
    ))
);

CREATE INDEX idx_column_profiles_run ON column_profiles (profiling_run_id);
CREATE INDEX idx_column_profiles_column ON column_profiles (dataset_column_id, metric_name);

-- ------------------------------------------------------------------
-- Data quality
-- ------------------------------------------------------------------

CREATE TABLE quality_suites (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    CONSTRAINT quality_suites_org_name_unique UNIQUE (organization_id, name)
);

CREATE TABLE quality_checks (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    dataset_id          UUID NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    suite_id            UUID REFERENCES quality_suites(id) ON DELETE SET NULL,
    name                TEXT NOT NULL,
    description         TEXT,
    check_type          TEXT NOT NULL,
    target_column       TEXT,                    -- column name when column-scoped
    parameters          JSONB NOT NULL DEFAULT '{}',
    severity            TEXT NOT NULL DEFAULT 'ERROR',
    expected_result     JSONB,
    schedule_cron       TEXT,
    enabled             BOOLEAN NOT NULL DEFAULT true,
    created_by          UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ,
    CONSTRAINT quality_checks_type_allowed CHECK (check_type IN (
        'NOT_NULL', 'UNIQUE', 'ROW_COUNT', 'ACCEPTED_VALUES', 'NUMERIC_RANGE',
        'REGEX', 'FRESHNESS', 'REFERENTIAL_INTEGRITY', 'SCHEMA', 'CUSTOM_SQL'
    )),
    CONSTRAINT quality_checks_severity_allowed CHECK (severity IN ('INFO', 'WARNING', 'ERROR', 'CRITICAL')),
    CONSTRAINT quality_checks_org_dataset_unique UNIQUE (organization_id, dataset_id, name)
);

CREATE INDEX idx_quality_checks_dataset ON quality_checks (dataset_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_quality_checks_org ON quality_checks (organization_id) WHERE deleted_at IS NULL;

CREATE TABLE quality_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    suite_id        UUID REFERENCES quality_suites(id) ON DELETE SET NULL,
    trigger         TEXT NOT NULL DEFAULT 'MANUAL',
    status          TEXT NOT NULL DEFAULT 'PENDING',
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ,
    duration_ms     BIGINT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT quality_runs_trigger_allowed CHECK (trigger IN ('MANUAL', 'SCHEDULED', 'API', 'SEED')),
    CONSTRAINT quality_runs_status_allowed CHECK (status IN (
        'PENDING', 'RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELLED'
    ))
);

CREATE INDEX idx_quality_runs_org ON quality_runs (organization_id, started_at DESC);

CREATE TABLE quality_results (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    quality_run_id  UUID NOT NULL REFERENCES quality_runs(id) ON DELETE CASCADE,
    check_id        UUID NOT NULL REFERENCES quality_checks(id) ON DELETE CASCADE,
    dataset_id      UUID NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    status          TEXT NOT NULL,
    rows_evaluated  BIGINT,
    rows_failed     BIGINT,
    actual_value    JSONB,
    expected_value  JSONB,
    duration_ms     BIGINT,
    error_message   TEXT,
    source_reference JSONB NOT NULL DEFAULT '{}',
    executed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT quality_results_status_allowed CHECK (status IN (
        'PASSED', 'FAILED', 'WARNING', 'ERROR', 'SKIPPED'
    ))
);

-- Immutable results history.
CREATE INDEX idx_quality_results_run ON quality_results (quality_run_id);
CREATE INDEX idx_quality_results_check ON quality_results (check_id, executed_at DESC);
CREATE INDEX idx_quality_results_dataset ON quality_results (dataset_id, executed_at DESC);

CREATE TABLE quality_incidents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    dataset_id      UUID NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    check_id        UUID REFERENCES quality_checks(id) ON DELETE SET NULL,
    quality_result_id UUID REFERENCES quality_results(id) ON DELETE SET NULL,
    incident_type   TEXT NOT NULL DEFAULT 'QUALITY_FAILURE',
    severity        TEXT NOT NULL DEFAULT 'ERROR',
    title           TEXT NOT NULL,
    summary         TEXT,
    status          TEXT NOT NULL DEFAULT 'OPEN',
    opened_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    acknowledged_at TIMESTAMPTZ,
    resolved_at     TIMESTAMPTZ,
    resolved_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    resolution_note TEXT,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT quality_incidents_status_allowed CHECK (status IN (
        'OPEN', 'ACKNOWLEDGED', 'RESOLVED', 'WONT_FIX'
    )),
    CONSTRAINT quality_incidents_severity_allowed CHECK (severity IN ('INFO', 'WARNING', 'ERROR', 'CRITICAL'))
);

CREATE INDEX idx_quality_incidents_org ON quality_incidents (organization_id, status);
CREATE INDEX idx_quality_incidents_dataset ON quality_incidents (dataset_id, opened_at DESC);

-- Freshness expectations and observed evidence (Part 17.3). Observed
-- freshness always records WHERE the observation came from.
CREATE TABLE freshness_records (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dataset_id          UUID NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    expected_interval_seconds INTEGER,
    expected_expression TEXT,                    -- e.g. cron, "every 1 hour"
    observed_at         TIMESTAMPTZ,             -- evidence timestamp from the source
    observation_source  TEXT,                    -- 'MAX_UPDATED_AT_COLUMN', 'INGESTION_RUN', ...
    freshness_seconds   BIGINT,                  -- observed_at - previous observed_at
    status              TEXT,                    -- 'FRESH' | 'WARNING' | 'STALE' | 'UNKNOWN'
    evaluated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT freshness_records_status_allowed CHECK (status IN ('FRESH', 'WARNING', 'STALE', 'UNKNOWN')),
    CONSTRAINT freshness_records_source_allowed CHECK (observation_source IN (
        'MAX_UPDATED_AT_COLUMN', 'INGESTION_RUN', 'OPENLINEAGE_EVENT', 'MANUAL', 'SEED'
    ))
);

CREATE INDEX idx_freshness_records_dataset ON freshness_records (dataset_id, evaluated_at DESC);
CREATE UNIQUE INDEX idx_freshness_records_dataset_latest ON freshness_records (dataset_id)
    WHERE status IS NOT NULL;
