-- 010: Authentication sessions and cross-organization membership lookup.

CREATE TABLE sessions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash    TEXT NOT NULL,                 -- SHA-256 of the opaque session token
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL,
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at    TIMESTAMPTZ,
    user_agent    TEXT,
    ip_address    TEXT,
    active_org_id UUID NOT NULL,                 -- currently selected organization
    CONSTRAINT sessions_token_hash_unique UNIQUE (token_hash)
);

CREATE INDEX idx_sessions_user ON sessions (user_id) WHERE revoked_at IS NULL;

-- Sessions are user-scoped (not organization-scoped) and intentionally have
-- no RLS policy: authentication must resolve before any tenant context
-- exists. Tenant authorization is enforced for every organization-owned
-- object through the session's active_org_id.

-- SECURITY DEFINER helper: membership lookup across organizations for a
-- user. memberships is FORCE-row-level-secured, so a cross-org read for
-- authentication requires elevation to the migration/owner role. The
-- function is read-only and exposes no other bypass.
CREATE OR REPLACE FUNCTION get_user_memberships(p_user_id UUID)
RETURNS TABLE (
    organization_id UUID,
    organization_name TEXT,
    organization_slug TEXT,
    role TEXT
)
LANGUAGE sql
SECURITY DEFINER
SET search_path = public
STABLE
AS $$
    SELECT o.id, o.name, o.slug, m.role
    FROM memberships m
    JOIN organizations o ON o.id = m.organization_id
    WHERE m.user_id = p_user_id
      AND m.deleted_at IS NULL
      AND o.deleted_at IS NULL;
$$;
