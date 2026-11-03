-- 007: Alerts, notifications, audit logs, API keys, webhooks.

CREATE TABLE alert_rules (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    condition_type  TEXT NOT NULL,
    parameters      JSONB NOT NULL DEFAULT '{}',
    severity        TEXT NOT NULL DEFAULT 'WARNING',
    enabled         BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    CONSTRAINT alert_rules_condition_allowed CHECK (condition_type IN (
        'INGESTION_FAILURE', 'SOURCE_CONNECTION_FAILURE', 'FRESHNESS_VIOLATION',
        'QUALITY_CHECK_FAILURE', 'SCHEMA_CHANGE', 'BREAKING_SCHEMA_CHANGE',
        'PROFILE_ANOMALY', 'PIPELINE_DURATION_INCREASE', 'MISSING_EXPECTED_JOB',
        'SENSITIVE_METADATA_DISCOVERY'
    )),
    CONSTRAINT alert_rules_severity_allowed CHECK (severity IN ('INFO', 'WARNING', 'ERROR', 'CRITICAL'))
);

CREATE TABLE alerts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    rule_id         UUID REFERENCES alert_rules(id) ON DELETE SET NULL,
    alert_type      TEXT NOT NULL,
    severity        TEXT NOT NULL DEFAULT 'WARNING',
    title           TEXT NOT NULL,
    summary         TEXT,
    entity_type     TEXT,                        -- 'dataset', 'data_source', 'job', ...
    entity_id       UUID,
    status          TEXT NOT NULL DEFAULT 'OPEN',
    assigned_to     UUID REFERENCES users(id) ON DELETE SET NULL,
    acknowledged_at TIMESTAMPTZ,
    acknowledged_by UUID REFERENCES users(id) ON DELETE SET NULL,
    resolved_at     TIMESTAMPTZ,
    resolved_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    resolution_note TEXT,
    dedupe_key      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT alerts_severity_allowed CHECK (severity IN ('INFO', 'WARNING', 'ERROR', 'CRITICAL')),
    CONSTRAINT alerts_status_allowed CHECK (status IN ('OPEN', 'ACKNOWLEDGED', 'RESOLVED'))
);

CREATE INDEX idx_alerts_org_status ON alerts (organization_id, status, created_at DESC);
CREATE INDEX idx_alerts_entity ON alerts (entity_type, entity_id);
CREATE UNIQUE INDEX idx_alerts_dedupe ON alerts (organization_id, dedupe_key)
    WHERE dedupe_key IS NOT NULL AND status <> 'RESOLVED';

CREATE TABLE notifications (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    alert_id        UUID REFERENCES alerts(id) ON DELETE CASCADE,
    user_id         UUID REFERENCES users(id) ON DELETE CASCADE,
    channel         TEXT NOT NULL DEFAULT 'IN_APP',
    title           TEXT NOT NULL,
    body            TEXT,
    read_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT notifications_channel_allowed CHECK (channel IN ('IN_APP', 'WEBHOOK'))
);

CREATE INDEX idx_notifications_user ON notifications (user_id, created_at DESC)
    WHERE read_at IS NULL;

CREATE TABLE audit_logs (
    id              BIGSERIAL PRIMARY KEY,
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    actor_kind      TEXT NOT NULL DEFAULT 'USER',
    action          TEXT NOT NULL,               -- e.g. 'source.created', 'check.executed'
    entity_type     TEXT NOT NULL,
    entity_id       UUID,
    before_state    JSONB,
    after_state     JSONB,
    request_id      TEXT,
    ip_address      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT audit_logs_actor_kind_allowed CHECK (actor_kind IN ('USER', 'API_KEY', 'SYSTEM', 'CONNECTOR'))
);

CREATE INDEX idx_audit_logs_org_time ON audit_logs (organization_id, created_at DESC);
CREATE INDEX idx_audit_logs_entity ON audit_logs (organization_id, entity_type, entity_id);

CREATE TABLE api_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    key_prefix      TEXT NOT NULL,               -- first chars for identification, e.g. 'da_1a2b'
    key_hash        TEXT NOT NULL,               -- SHA-256 of the full key; raw key shown once
    scopes          TEXT[] NOT NULL DEFAULT '{}',
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    last_used_at    TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT api_keys_prefix_hash_unique UNIQUE (key_hash)
);

CREATE INDEX idx_api_keys_org ON api_keys (organization_id) WHERE revoked_at IS NULL;

CREATE TABLE webhooks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    url             TEXT NOT NULL,
    secret_hash     TEXT,
    events          TEXT[] NOT NULL DEFAULT '{}',
    enabled         BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ,
    CONSTRAINT webhooks_url_scheme CHECK (url ~ '^https?://')
);
