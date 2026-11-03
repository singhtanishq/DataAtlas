-- 002: Data source registry, connector framework and ingestion entities.

CREATE TABLE connector_definitions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key         TEXT NOT NULL,                -- e.g. 'postgres', 'csv_file', 'dbt_artifacts'
    display_name TEXT NOT NULL,
    platform    TEXT NOT NULL,                -- platform family, e.g. 'postgresql', 'file', 'dbt'
    config_schema JSONB NOT NULL DEFAULT '{}', -- JSON Schema for connector configuration
    capabilities JSONB NOT NULL DEFAULT '[]', -- discovery/profiling/quality/lineage flags
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT connector_definitions_key_unique UNIQUE (key)
);

CREATE TABLE data_sources (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    connector_key   TEXT NOT NULL REFERENCES connector_definitions(key),
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'REGISTERED',
    description     TEXT,
    config          JSONB NOT NULL DEFAULT '{}', -- non-secret connection configuration
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    CONSTRAINT data_sources_org_slug_unique UNIQUE (organization_id, slug),
    CONSTRAINT data_sources_status_allowed CHECK (status IN (
        'REGISTERED', 'CONFIGURED', 'VALIDATING', 'ACTIVE', 'INGESTING',
        'PAUSED', 'DISCONNECTED', 'FAILED'
    ))
);

CREATE INDEX idx_data_sources_org ON data_sources (organization_id) WHERE deleted_at IS NULL;

-- Status history for connector lifecycle auditing.
CREATE TABLE data_source_status_history (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    data_source_id  UUID NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    from_status     TEXT,
    to_status       TEXT NOT NULL,
    reason          TEXT,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_ds_status_history_source ON data_source_status_history (data_source_id, changed_at DESC);

-- Source credentials are stored encrypted (ciphertext only, AES-256-GCM).
-- The key lives in APP_ENCRYPTION_KEY; plaintext never touches the database.
CREATE TABLE source_credentials (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    data_source_id      UUID NOT NULL UNIQUE REFERENCES data_sources(id) ON DELETE CASCADE,
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    credential_name     TEXT NOT NULL DEFAULT 'default',
    encryption_algo     TEXT NOT NULL DEFAULT 'AES-256-GCM',
    key_version         INTEGER NOT NULL DEFAULT 1,
    ciphertext          BYTEA NOT NULL,
    nonce               BYTEA NOT NULL,
    rotated_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ingestion_pipelines (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    data_source_id  UUID NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    pipeline_type   TEXT NOT NULL DEFAULT 'METADATA_DISCOVERY',
    status          TEXT NOT NULL DEFAULT 'ACTIVE',
    config          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    CONSTRAINT ingestion_pipelines_type_allowed CHECK (pipeline_type IN (
        'METADATA_DISCOVERY', 'PROFILE', 'QUALITY', 'LINEAGE', 'USAGE'
    )),
    CONSTRAINT ingestion_pipelines_status_allowed CHECK (status IN (
        'ACTIVE', 'PAUSED', 'DISABLED'
    )),
    CONSTRAINT ingestion_pipelines_org_source_unique UNIQUE (organization_id, data_source_id, name)
);

CREATE TABLE ingestion_schedules (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_id     UUID NOT NULL UNIQUE REFERENCES ingestion_pipelines(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    cron_expression TEXT,
    interval_seconds INTEGER,
    timezone        TEXT NOT NULL DEFAULT 'UTC',
    enabled         BOOLEAN NOT NULL DEFAULT true,
    next_run_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ingestion_schedules_cron_or_interval CHECK (
        cron_expression IS NOT NULL OR interval_seconds IS NOT NULL
    )
);

CREATE INDEX idx_ingestion_schedules_due ON ingestion_schedules (enabled, next_run_at)
    WHERE enabled = true;

CREATE TABLE ingestion_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_id     UUID NOT NULL REFERENCES ingestion_pipelines(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    run_token       UUID NOT NULL DEFAULT gen_random_uuid(),
    trigger         TEXT NOT NULL DEFAULT 'MANUAL',
    status          TEXT NOT NULL DEFAULT 'PENDING',
    started_at      TIMESTAMPTZ,
    finished_at     TIMESTAMPTZ,
    duration_ms     BIGINT,
    error_class     TEXT,
    error_message   TEXT,
    progress        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ingestion_runs_trigger_allowed CHECK (trigger IN (
        'MANUAL', 'SCHEDULED', 'API', 'SEED'
    )),
    CONSTRAINT ingestion_runs_status_allowed CHECK (status IN (
        'PENDING', 'RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELLED'
    ))
);

-- Immutable run history: finished runs are never updated again.
CREATE UNIQUE INDEX idx_ingestion_runs_token ON ingestion_runs (run_token);
CREATE INDEX idx_ingestion_runs_pipeline ON ingestion_runs (pipeline_id, created_at DESC);
CREATE INDEX idx_ingestion_runs_org_recent ON ingestion_runs (organization_id, created_at DESC);
CREATE INDEX idx_ingestion_runs_status ON ingestion_runs (organization_id, status) WHERE status IN ('PENDING', 'RUNNING');

-- Ingestion run log lines (bounded; raw payloads live outside the catalog).
CREATE TABLE ingestion_run_logs (
    id              BIGSERIAL PRIMARY KEY,
    run_id          UUID NOT NULL REFERENCES ingestion_runs(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL,
    logged_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    level           TEXT NOT NULL DEFAULT 'INFO',
    message         TEXT NOT NULL,
    details         JSONB
);

CREATE INDEX idx_ingestion_run_logs_run ON ingestion_run_logs (run_id, logged_at);

-- Checkpoints let connectors resume incremental discovery.
CREATE TABLE connector_checkpoints (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_id     UUID NOT NULL REFERENCES ingestion_pipelines(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    checkpoint_key  TEXT NOT NULL,
    checkpoint_value JSONB NOT NULL DEFAULT '{}',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT connector_checkpoints_pipeline_key_unique UNIQUE (pipeline_id, checkpoint_key)
);
