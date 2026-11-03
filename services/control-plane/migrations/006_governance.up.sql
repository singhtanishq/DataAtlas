-- 006: Governance — glossary, tags, ownership, classification.

CREATE TABLE business_glossaries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL UNIQUE REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE glossary_terms (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    glossary_id     UUID NOT NULL REFERENCES business_glossaries(id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    parent_term_id  UUID REFERENCES glossary_terms(id) ON DELETE SET NULL,
    name            TEXT NOT NULL,
    definition      TEXT NOT NULL,
    category        TEXT,
    synonyms        TEXT[] NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'DRAFT',
    owner_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    owner_team_id   UUID REFERENCES teams(id) ON DELETE SET NULL,
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    CONSTRAINT glossary_terms_org_name_unique UNIQUE (organization_id, name),
    CONSTRAINT glossary_terms_status_allowed CHECK (status IN ('DRAFT', 'PUBLISHED', 'DEPRECATED')),
    CONSTRAINT glossary_terms_no_self_parent CHECK (parent_term_id IS NULL OR parent_term_id <> id)
);

CREATE INDEX idx_glossary_terms_org ON glossary_terms (organization_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_glossary_terms_name_trgm ON glossary_terms USING gin (name gin_trgm_ops);

CREATE TABLE glossary_term_relationships (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    from_term_id    UUID NOT NULL REFERENCES glossary_terms(id) ON DELETE CASCADE,
    to_term_id      UUID NOT NULL REFERENCES glossary_terms(id) ON DELETE CASCADE,
    relationship    TEXT NOT NULL DEFAULT 'RELATED',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT glossary_term_rel_type_allowed CHECK (relationship IN (
        'RELATED', 'SYNONYM_OF', 'CONTAINS', 'DERIVED_FROM'
    )),
    CONSTRAINT glossary_term_rel_no_self CHECK (from_term_id <> to_term_id),
    CONSTRAINT glossary_term_rel_unique UNIQUE (from_term_id, to_term_id, relationship)
);

-- Term-to-asset associations.
CREATE TABLE glossary_term_assets (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    term_id     UUID NOT NULL REFERENCES glossary_terms(id) ON DELETE CASCADE,
    asset_id    UUID NOT NULL REFERENCES data_assets(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT glossary_term_assets_unique UNIQUE (term_id, asset_id)
);

CREATE INDEX idx_glossary_term_assets_asset ON glossary_term_assets (asset_id);

CREATE TABLE tags (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    color           TEXT,
    is_system       BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tags_org_name_unique UNIQUE (organization_id, name)
);

CREATE TABLE asset_tags (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tag_id      UUID NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    asset_id    UUID NOT NULL REFERENCES data_assets(id) ON DELETE CASCADE,
    assigned_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT asset_tags_unique UNIQUE (tag_id, asset_id)
);

CREATE INDEX idx_asset_tags_asset ON asset_tags (asset_id);

-- Ownership assignments with history (Part 18.1). Current assignment =
-- latest row with unassigned_at IS NULL.
CREATE TABLE ownership_assignments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    asset_id        UUID NOT NULL REFERENCES data_assets(id) ON DELETE CASCADE,
    owner_type      TEXT NOT NULL,
    owner_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    owner_team_id   UUID REFERENCES teams(id) ON DELETE SET NULL,
    ownership_role  TEXT NOT NULL DEFAULT 'TECHNICAL_OWNER',
    assigned_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    assigned_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    unassigned_at   TIMESTAMPTZ,
    CONSTRAINT ownership_role_allowed CHECK (ownership_role IN (
        'TECHNICAL_OWNER', 'BUSINESS_OWNER', 'DATA_STEWARD'
    )),
    CONSTRAINT ownership_type_allowed CHECK (owner_type IN ('USER', 'TEAM')),
    CONSTRAINT ownership_requires_owner CHECK (
        (owner_type = 'USER' AND owner_user_id IS NOT NULL)
        OR (owner_type = 'TEAM' AND owner_team_id IS NOT NULL)
    )
);

CREATE INDEX idx_ownership_asset ON ownership_assignments (asset_id)
    WHERE unassigned_at IS NULL;
CREATE INDEX idx_ownership_org ON ownership_assignments (organization_id);

-- ------------------------------------------------------------------
-- Classification (Part 18.4) — platform labels, not legal determinations.
-- ------------------------------------------------------------------

CREATE TABLE classification_rules (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    rule_type       TEXT NOT NULL DEFAULT 'COLUMN_NAME_PATTERN',
    pattern         TEXT,
    pattern_syntax  TEXT NOT NULL DEFAULT 'regex',
    target_type     TEXT NOT NULL DEFAULT 'COLUMN_NAME',
    classification  TEXT NOT NULL,
    confidence      TEXT NOT NULL DEFAULT 'MEDIUM',
    enabled         BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT classification_rules_type_allowed CHECK (rule_type IN (
        'COLUMN_NAME_PATTERN', 'DATA_TYPE_MATCH', 'MANUAL'
    )),
    CONSTRAINT classification_rules_class_allowed CHECK (classification IN (
        'PUBLIC', 'INTERNAL', 'CONFIDENTIAL', 'RESTRICTED', 'SENSITIVE'
    )),
    CONSTRAINT classification_rules_confidence_allowed CHECK (confidence IN ('LOW', 'MEDIUM', 'HIGH'))
);

CREATE TABLE asset_classifications (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    asset_id            UUID NOT NULL REFERENCES data_assets(id) ON DELETE CASCADE,
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    classification      TEXT NOT NULL,
    source              TEXT NOT NULL DEFAULT 'MANUAL',
    rule_id             UUID REFERENCES classification_rules(id) ON DELETE SET NULL,
    confidence          TEXT NOT NULL DEFAULT 'MEDIUM',
    review_status       TEXT NOT NULL DEFAULT 'UNREVIEWED',
    classified_by       UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT asset_classification_class_allowed CHECK (classification IN (
        'PUBLIC', 'INTERNAL', 'CONFIDENTIAL', 'RESTRICTED', 'SENSITIVE'
    )),
    CONSTRAINT asset_classification_source_allowed CHECK (source IN ('MANUAL', 'RULE', 'SEED')),
    CONSTRAINT asset_classification_review_allowed CHECK (review_status IN (
        'UNREVIEWED', 'CONFIRMED', 'REJECTED'
    ))
);

CREATE INDEX idx_asset_classifications_asset ON asset_classifications (asset_id);
CREATE UNIQUE INDEX idx_asset_classifications_effective ON asset_classifications (asset_id, classification);

CREATE TABLE column_classifications (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    column_id           UUID NOT NULL REFERENCES dataset_columns(id) ON DELETE CASCADE,
    organization_id     UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    classification      TEXT NOT NULL,
    source              TEXT NOT NULL DEFAULT 'MANUAL',
    rule_id             UUID REFERENCES classification_rules(id) ON DELETE SET NULL,
    detection_method    TEXT,                   -- 'column_name_pattern', 'sampling', ...
    confidence          TEXT NOT NULL DEFAULT 'MEDIUM',
    review_status       TEXT NOT NULL DEFAULT 'UNREVIEWED',
    classified_by       UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT column_classification_class_allowed CHECK (classification IN (
        'PUBLIC', 'INTERNAL', 'CONFIDENTIAL', 'RESTRICTED', 'SENSITIVE'
    )),
    CONSTRAINT column_classification_review_allowed CHECK (review_status IN (
        'UNREVIEWED', 'CONFIRMED', 'REJECTED'
    ))
);

CREATE INDEX idx_column_classifications_column ON column_classifications (column_id);
CREATE UNIQUE INDEX idx_column_classifications_effective ON column_classifications (column_id, classification);
