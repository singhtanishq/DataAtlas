-- 003: Catalog assets, datasets and schema version history.
--
-- data_assets is the polymorphic registry of catalogued assets; datasets
-- extend it with dataset-specific state. schema_versions/schema_columns are
-- the immutable historical record; dataset_columns is the current state plus
-- user-authored business annotations.

CREATE TABLE data_assets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    data_source_id  UUID REFERENCES data_sources(id) ON DELETE SET NULL,
    asset_type      TEXT NOT NULL,
    namespace       TEXT NOT NULL,
    name            TEXT NOT NULL,
    display_name    TEXT,
    full_name       TEXT GENERATED ALWAYS AS (namespace || '.' || name) STORED,
    description     TEXT,                       -- merged business description
    native_description TEXT,                    -- description from the source system
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    last_seen_at    TIMESTAMPTZ,                -- last time a connector observed it
    native_metadata JSONB NOT NULL DEFAULT '{}', -- source-specific details, preserved
    CONSTRAINT data_assets_type_allowed CHECK (asset_type IN (
        'TABLE', 'VIEW', 'MATERIALIZED_VIEW', 'FILE', 'DBT_MODEL', 'DBT_SOURCE',
        'DBT_SEED', 'DBT_SNAPSHOT', 'JOB', 'DASHBOARD', 'EXTERNAL'
    )),
    -- Canonical asset identity within an organization (Part 10.4): one
    -- namespace/name per source and asset type.
    CONSTRAINT data_assets_org_source_ns_name_type_unique
        UNIQUE (organization_id, data_source_id, namespace, name, asset_type)
);

CREATE INDEX idx_data_assets_org ON data_assets (organization_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_data_assets_source ON data_assets (data_source_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_data_assets_type ON data_assets (organization_id, asset_type) WHERE deleted_at IS NULL;
CREATE INDEX idx_data_assets_name_trgm ON data_assets USING gin (name gin_trgm_ops);
CREATE INDEX idx_data_assets_full_name ON data_assets (organization_id, full_name) WHERE deleted_at IS NULL;

CREATE TABLE datasets (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    asset_id            UUID NOT NULL UNIQUE REFERENCES data_assets(id) ON DELETE CASCADE,
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    current_version_id  UUID,                   -- FK added after dataset_versions exists
    row_count           BIGINT,
    size_bytes          BIGINT,
    column_count        INTEGER,
    row_count_method    TEXT,                   -- exact | estimate | sample | inferred
    database_name       TEXT,
    schema_name         TEXT,
    location            TEXT,                   -- file path / URI for file-based assets
    content_checksum    TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at          TIMESTAMPTZ,
    CONSTRAINT datasets_row_count_method_allowed CHECK (
        row_count_method IS NULL OR row_count_method IN ('exact', 'estimate', 'sample', 'inferred')
    )
);

CREATE INDEX idx_datasets_org ON datasets (organization_id) WHERE deleted_at IS NULL;

-- A dataset version pins one schema snapshot plus dataset-level state at
-- observation time. The schema content itself lives in schema_versions.
CREATE TABLE schema_versions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dataset_id      UUID NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    version_number  INTEGER NOT NULL,
    checksum        TEXT NOT NULL,
    change_summary  JSONB NOT NULL DEFAULT '{}', -- added/removed/changed columns
    detected_by     TEXT NOT NULL DEFAULT 'CONNECTOR',
    is_breaking     BOOLEAN NOT NULL DEFAULT false,
    observed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT schema_versions_dataset_version_unique UNIQUE (dataset_id, version_number),
    CONSTRAINT schema_versions_detected_by_allowed CHECK (
        detected_by IN ('CONNECTOR', 'DBT', 'OPENLINEAGE', 'MANUAL', 'SEED')
    )
);

CREATE INDEX idx_schema_versions_dataset ON schema_versions (dataset_id, version_number DESC);

CREATE TABLE dataset_versions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dataset_id          UUID NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    schema_version_id   UUID NOT NULL REFERENCES schema_versions(id) ON DELETE CASCADE,
    version_number      INTEGER NOT NULL,
    row_count           BIGINT,
    size_bytes          BIGINT,
    content_checksum    TEXT,
    change_summary      JSONB NOT NULL DEFAULT '{}',
    observed_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT dataset_versions_dataset_version_unique UNIQUE (dataset_id, version_number)
);

CREATE INDEX idx_dataset_versions_dataset ON dataset_versions (dataset_id, version_number DESC);

ALTER TABLE datasets
    ADD CONSTRAINT datasets_current_version_fk
    FOREIGN KEY (current_version_id) REFERENCES dataset_versions(id) ON DELETE SET NULL;

-- Immutable column snapshot within a schema version.
CREATE TABLE schema_columns (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    schema_version_id   UUID NOT NULL REFERENCES schema_versions(id) ON DELETE CASCADE,
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    ordinal             INTEGER NOT NULL,
    data_type           TEXT NOT NULL,
    native_type         TEXT,
    nullable            BOOLEAN NOT NULL DEFAULT true,
    default_value       TEXT,
    is_primary_key      BOOLEAN NOT NULL DEFAULT false,
    is_foreign_key      BOOLEAN NOT NULL DEFAULT false,
    fk_reference        TEXT,
    description         TEXT,
    native_metadata     JSONB NOT NULL DEFAULT '{}',
    CONSTRAINT schema_columns_version_ordinal_unique UNIQUE (schema_version_id, ordinal)
);

CREATE INDEX idx_schema_columns_version ON schema_columns (schema_version_id, ordinal);

-- Current column state (mirrors the latest schema version) plus
-- user-authored annotations. Ingestion merges new schema versions into this
-- table using an explicit policy that never overwrites user descriptions.
CREATE TABLE dataset_columns (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    dataset_id          UUID NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    ordinal             INTEGER NOT NULL,
    data_type           TEXT NOT NULL,
    native_type         TEXT,
    nullable            BOOLEAN NOT NULL DEFAULT true,
    default_value       TEXT,
    is_primary_key      BOOLEAN NOT NULL DEFAULT false,
    is_foreign_key      BOOLEAN NOT NULL DEFAULT false,
    fk_reference        TEXT,
    description         TEXT,                   -- user-authored; ingestion never overwrites
    native_description  TEXT,
    first_seen_version  UUID REFERENCES schema_versions(id) ON DELETE SET NULL,
    last_seen_version   UUID REFERENCES schema_versions(id) ON DELETE SET NULL,
    status              TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT dataset_columns_status_allowed CHECK (status IN ('ACTIVE', 'REMOVED'))
);

CREATE UNIQUE INDEX idx_dataset_columns_current ON dataset_columns (dataset_id, name)
    WHERE status = 'ACTIVE';
CREATE INDEX idx_dataset_columns_org_name ON dataset_columns (organization_id, name)
    WHERE status = 'ACTIVE';
CREATE INDEX idx_dataset_columns_name_trgm ON dataset_columns USING gin (name gin_trgm_ops);
