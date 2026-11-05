# Threat Model

STRIDE-based analysis of the DataAtlas platform. This document describes
trust boundaries, assets, threats and implemented mitigations. It is an
engineering artifact, not a security certification or compliance claim.

## 1. System components and trust boundaries

```text
  Boundary B1 (user/browser)          Boundary B2 (control plane)
  ┌──────────────────┐   HTTPS/HTTP  ┌──────────────────────────┐
  │  Next.js web     │ ────────────▶ │  Go control plane API    │
  │  dashboard       │  session      │  (auth, catalog, lineage,│
  └──────────────────┘  cookie       │   quality, governance)   │
                                     └────────────┬─────────────┘
  Boundary B3 (workers)                            │ SQL (GUC-scoped)
  ┌──────────────────┐   NATS JetStream ┌──────────▼───────────┐
  │  Python workers  │ ◀──────────────▶ │  PostgreSQL metadata  │
  │  (ingest, probe, │  request/reply,  │  store (RLS enforced) │
  │   quality)       │  durable jobs    └──────────────────────┘
  └───────┬──────────┘
          │ read-only sessions          Boundary B4 (external)
          ▼                              ┌──────────────────────┐
  ┌──────────────────────┐               │  Customer data       │
  │  External data       │ ◀─────────────│  systems (databases, │
  │  sources             │   read-only   │  files, BI tools)    │
  └──────────────────────┘               └──────────────────────┘
```

## 2. Assets

| Asset | Sensitivity | Location |
|---|---|---|
| Source credentials (passwords) | Critical — encrypted at rest (AES-256-GCM) | `source_credentials` (ciphertext only) |
| `APP_ENCRYPTION_KEY` | Critical | Environment (never persisted, never logged) |
| Session tokens | High (stored SHA-256 hashed) | `sessions` |
| Password hashes | High (Argon2id) | `users` |
| Metadata (schemas, lineage, profiles) | High | PostgreSQL |
| API keys | High (SHA-256 hashed, prefix only displayed) | `api_keys` |
| Row-level data from sources | Never ingested by design | — |

## 3. STRIDE analysis

### Spoofing

| Threat | Mitigation |
|---|---|
| Forged sessions | Opaque 256-bit tokens; only SHA-256 hashes stored; HttpOnly + SameSite=Lax cookies; expiry and revocation |
| Credential stuffing | Argon2id with OWASP parameters; comparable-cost dummy verification for unknown emails; per-IP fixed-window rate limiting on auth endpoints (20/min) |
| API key spoofing | API keys hashed at rest; prefix-only display; key material shown exactly once at creation |

### Tampering

| Threat | Mitigation |
|---|---|
| Cross-tenant data modification | Row-Level Security with `FORCE` on all org-scoped tables; transaction-bound `app.current_organization_id` GUC; `WITH CHECK` rejects foreign-tenant writes (verified by integration tests) |
| Malicious OpenLineage/dbt payloads | Strict schema validation, size limits (1 MiB events / 4 MiB manifests), exactly-one-statement parsing, payload length caps; unknown datasets land as non-privileged EXTERNAL assets |
| Credential ciphertext tampering | AES-256-GCM authenticated encryption — tampered ciphertext fails decryption with a generic error |

### Repudiation

| Threat | Mitigation |
|---|---|
| Disputed governance actions | `audit_logs` record actor identity, action, entity and timestamps for classification and glossary changes |
| Disputed source registration | Source status transitions recorded in `data_source_status_history` with reasons |

### Information disclosure

| Threat | Mitigation |
|---|---|
| Credential leakage via API | Credentials never returned by any endpoint; never rendered in the UI; decryption exists only in the worker path |
| Credential leakage via broker | NATS messages carry source IDs, never secrets; workers fetch ciphertext from PostgreSQL inside tenant-bound transactions |
| Cross-tenant reads | RLS `USING` policies fail closed (no GUC → zero rows, verified); API queries additionally filter by organization; cross-tenant 404s |
| Raw data exposure | Profiling exports aggregates only; text top-values are hashed; quality results store counts, never rows |
| Timing-based account enumeration | Unknown-email logins burn comparable verification time |
| Error-message leakage | Authentication errors are generic; connection errors to sources are sanitized (password mentions redacted) |

### Denial of service

| Threat | Mitigation |
|---|---|
| Unbounded source queries | Every connector session sets `statement_timeout` and `default_transaction_read_only`; profiling samples bounded by `LIMIT`; quality uses bounded samples |
| Resource-exhausting custom SQL | SQLGlot-validated single SELECT, wrapped in a bounded count; quality row limits; per-run timeouts |
| API flooding | Per-IP rate limiting on auth; request limits on lineage payloads; JetStream `max_deliver` caps redelivery |
| Worker starvation | Durable pull consumers with explicit acks; bounded pools; `max_deliver=5` with nak backoff |

### Elevation of privilege

| Threat | Mitigation |
|---|---|
| Role escalation through API | Role matrix enforced server-side per permission (`Authorize`); OWNER-only org management |
| SQL injection through metadata | Identifiers validated against a strict allowlist and quoted before any interpolation; all values bound as parameters |
| RLS bypass via superuser | Application connects as `dataatlas_app` (NOSUPERUSER, NOBYPASSRLS); the owner role is reserved for migrations |
| Arbitrary code execution via worker config | Connector config is data, not code; only allowlisted connector classes are instantiated |

## 4. Residual risks

- **Local development secrets**: the demo stack ships with documented
  local-only credentials (SeaweedFS dev keys, scanner/app roles). These are
  for local demonstration only and must be replaced in any shared or
  production deployment.
- **NATS transport**: jobs are not encrypted in transit on the local
  loopback. Production deployments should enable NATS TLS and authentication.
- **`SECURITY DEFINER` membership lookup**: `get_user_memberships` bypasses
  RLS for cross-organization authentication reads. It is read-only and
  exposes only org id/name/slug/role for the requesting user, but must be
  reviewed if the function is extended.
- **Custom SQL**: SQLGlot validation is strong but parser-based validation
  can never be complete. CUSTOM_SQL remains a privileged capability (admin
  roles create checks; execution is read-only and bounded).
- **No MFA/SSO yet**; single-factor sessions with Argon2id password policy.
- **Rate limiting is per-instance**; multi-replica deployments need a
  shared limiter (Redis) — noted in the roadmap.

## 5. Verification

- `tests/rls_integration_test.go`: cross-tenant invisibility, fail-closed
  behavior, WITH CHECK rejection, join-derived policy coverage.
- `tests/auth_integration_test.go`: session lifecycle, wrong-password
  rejection, cross-org switch rejection (403), unauthenticated 401.
- `tests/sources_integration_test.go`: credential non-disclosure in every
  response; cross-tenant source access 404.
- `tests/lineage_integration_test.go`: cross-tenant lineage 404.
- `tests/test_quality_integration.py`: custom SQL rejection matrix
  (multi-statement, INSERT, DELETE, DROP).
- `tests/test_crypto.py`: Go→Python AES-GCM interop, tamper rejection.
