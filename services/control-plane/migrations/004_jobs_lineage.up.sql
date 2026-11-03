-- 004: Jobs, job runs and the lineage graph (PostgreSQL edge tables).

CREATE TABLE jobs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    data_source_id  UUID REFERENCES data_sources(id) ON DELETE SET NULL,
    asset_id        UUID REFERENCES data_assets(id) ON DELETE SET NULL,
    namespace       TEXT NOT NULL,
    name            TEXT NOT NULL,
    display_name    TEXT,
    description     TEXT,
    job_type        TEXT NOT NULL DEFAULT 'BATCH',
    scheduler       TEXT,                        -- e.g. 'airflow', 'dbt', 'openlineage', 'dataatlas'
    schedule        TEXT,
    owner           TEXT,
    status          TEXT NOT NULL DEFAULT 'ACTIVE',
    native_metadata JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    CONSTRAINT jobs_org_ns_name_unique UNIQUE (organization_id, namespace, name),
    CONSTRAINT jobs_status_allowed CHECK (status IN ('ACTIVE', 'RETIRED', 'ORPHANED'))
);

CREATE INDEX idx_jobs_org ON jobs (organization_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_jobs_name_trgm ON jobs USING gin (name gin_trgm_ops);

CREATE TABLE job_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id          UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    run_id          TEXT NOT NULL,               -- external run identifier (OpenLineage runId)
    status          TEXT NOT NULL DEFAULT 'PENDING',
    started_at      TIMESTAMPTZ,
    ended_at        TIMESTAMPTZ,
    duration_ms     BIGINT,
    error_message   TEXT,
    error_facet     JSONB,
    source_facets   JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT job_runs_status_allowed CHECK (status IN (
        'PENDING', 'RUNNING', 'SUCCEEDED', 'FAILED', 'ABORTED', 'UNKNOWN'
    )),
    -- Idempotent event processing: one row per external run identity.
    CONSTRAINT job_runs_org_job_run_unique UNIQUE (organization_id, job_id, run_id)
);

CREATE INDEX idx_job_runs_job ON job_runs (job_id, started_at DESC);
CREATE INDEX idx_job_runs_org_status ON job_runs (organization_id, status);
CREATE INDEX idx_job_runs_org_time ON job_runs (organization_id, started_at DESC);

CREATE TABLE job_inputs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id      UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    dataset_id  UUID NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT job_inputs_job_dataset_unique UNIQUE (job_id, dataset_id)
);

CREATE TABLE job_outputs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id      UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    dataset_id  UUID NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT job_outputs_job_dataset_unique UNIQUE (job_id, dataset_id)
);

CREATE INDEX idx_job_inputs_dataset ON job_inputs (dataset_id);
CREATE INDEX idx_job_outputs_dataset ON job_outputs (dataset_id);

-- Dataset-level lineage edges. Deduplicated per (source, target, type,
-- producing job); re-observation refreshes last_seen_at instead of
-- duplicating rows.
CREATE TABLE lineage_edges (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    source_asset_id     UUID NOT NULL REFERENCES data_assets(id) ON DELETE CASCADE,
    target_asset_id     UUID NOT NULL REFERENCES data_assets(id) ON DELETE CASCADE,
    relationship_type   TEXT NOT NULL DEFAULT 'TRANSFORMATION',
    job_id              UUID REFERENCES jobs(id) ON DELETE SET NULL,
    job_run_id          UUID REFERENCES job_runs(id) ON DELETE SET NULL,
    origin              TEXT NOT NULL DEFAULT 'OPENLINEAGE',
    confidence          TEXT NOT NULL DEFAULT 'EXPLICIT',
    description         TEXT,
    first_seen_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    event_reference     JSONB NOT NULL DEFAULT '{}',
    CONSTRAINT lineage_edges_type_allowed CHECK (relationship_type IN (
        'TRANSFORMATION', 'SYNONYM', 'MANUAL', 'CONSUMES'
    )),
    CONSTRAINT lineage_edges_origin_allowed CHECK (origin IN (
        'OPENLINEAGE', 'DBT', 'SQL_PARSE', 'MANUAL', 'SEED', 'CONNECTOR'
    )),
    -- Evidence status: EXPLICIT = asserted by an authoritative event;
    -- INFERRED = derived heuristically. Never displayed as verified.
    CONSTRAINT lineage_edges_confidence_allowed CHECK (confidence IN (
        'EXPLICIT', 'INFERRED'
    )),
    CONSTRAINT lineage_edges_no_self_loop CHECK (source_asset_id <> target_asset_id)
);

CREATE UNIQUE INDEX idx_lineage_edges_dedupe ON lineage_edges (
    organization_id, source_asset_id, target_asset_id,
    COALESCE(job_id, '00000000-0000-0000-0000-000000000000'::uuid), relationship_type
);
CREATE INDEX idx_lineage_edges_source ON lineage_edges (organization_id, source_asset_id);
CREATE INDEX idx_lineage_edges_target ON lineage_edges (organization_id, target_asset_id);

-- Column-level lineage, only where confidently established.
CREATE TABLE column_lineage_edges (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    lineage_edge_id     UUID REFERENCES lineage_edges(id) ON DELETE CASCADE,
    source_column_id    UUID REFERENCES dataset_columns(id) ON DELETE CASCADE,
    target_column_id    UUID NOT NULL REFERENCES dataset_columns(id) ON DELETE CASCADE,
    transformation      TEXT,
    origin              TEXT NOT NULL DEFAULT 'OPENLINEAGE',
    confidence          TEXT NOT NULL DEFAULT 'EXPLICIT',
    first_seen_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT column_lineage_confidence_allowed CHECK (confidence IN ('EXPLICIT', 'INFERRED')),
    CONSTRAINT column_lineage_no_self CHECK (source_column_id IS NULL OR source_column_id <> target_column_id)
);

CREATE UNIQUE INDEX idx_column_lineage_dedupe ON column_lineage_edges (
    organization_id, COALESCE(source_column_id, '00000000-0000-0000-0000-000000000000'::uuid),
    target_column_id, COALESCE(lineage_edge_id, '00000000-0000-0000-0000-000000000000'::uuid)
);
CREATE INDEX idx_column_lineage_target ON column_lineage_edges (organization_id, target_column_id);
CREATE INDEX idx_column_lineage_source ON column_lineage_edges (organization_id, source_column_id);
