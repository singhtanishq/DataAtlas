# API Reference

Base URL (local development): `http://localhost:8080`

All endpoints are authenticated with the `dataatlas_session` HTTP-only
cookie (browser flows) and return JSON. Errors use the envelope:

```json
{ "error": { "code": "not_found", "message": "…", "request_id": "…" } }
```

## Authentication

| Method | Path | Description |
|---|---|---|
| POST | `/v1/auth/register` | Create user + organization + OWNER membership; sets session cookie |
| POST | `/v1/auth/login` | Verify credentials (Argon2id); sets session cookie |
| POST | `/v1/auth/logout` | Revoke the session |
| GET | `/v1/auth/me` | Current identity (user, organization, role) |
| POST | `/v1/auth/organizations/switch` | Switch active organization (membership required) |

## Data sources

| Method | Path | Permission | Description |
|---|---|---|---|
| POST | `/v1/sources` | sources:manage | Register source; credentials encrypted (AES-256-GCM) |
| GET | `/v1/sources` | read | List sources (never includes credentials) |
| GET | `/v1/sources/{id}` | read | Source detail |
| DELETE | `/v1/sources/{id}` | sources:manage | Soft delete |
| POST | `/v1/sources/{id}/test` | sources:manage | Connection test via worker (NATS round-trip) |
| POST | `/v1/sources/{id}/discover` | ingestion:trigger | Queue durable metadata discovery run |

## Catalog

| Method | Path | Permission | Description |
|---|---|---|---|
| GET | `/v1/catalog/datasets?q&type&limit&offset` | read | Server-side search with pagination |
| GET | `/v1/catalog/datasets/{id}` | read | Full details (version, owners, tags) |
| GET | `/v1/catalog/datasets/{id}/columns` | read | Active columns with PK/FK evidence |
| PATCH | `/v1/catalog/datasets/{id}` | metadata:edit | Update business description |
| POST | `/v1/catalog/datasets/{id}/owners` | metadata:edit | Assign ownership (history preserved) |
| POST | `/v1/catalog/datasets/{id}/tags` | metadata:edit | Attach tag |
| DELETE | `/v1/catalog/datasets/{id}/tags/{tagID}` | metadata:edit | Detach tag |
| POST | `/v1/catalog/datasets/{id}/profile` | ingestion:trigger | Queue profiling run |
| GET | `/v1/catalog/datasets/{id}/profile` | read | Latest profiling snapshot with provenance |
| GET | `/v1/catalog/datasets/{id}/profiles` | read | Profiling history |
| POST | `/v1/catalog/datasets/{id}/freshness` | quality:manage | Record freshness expectation |

## Ingestion runs

| Method | Path | Description |
|---|---|---|
| GET | `/v1/ingestion/runs/{id}` | Run status with progress payload |
| GET | `/v1/sources/{id}/runs` | Run history for a source |

## Quality

| Method | Path | Permission | Description |
|---|---|---|---|
| POST | `/v1/quality/checks` | quality:manage | Create check (10 types incl. validated CUSTOM_SQL) |
| GET | `/v1/quality/checks?dataset_id` | read | List checks for a dataset |
| PATCH | `/v1/quality/checks/{id}` | quality:manage | Partial update |
| DELETE | `/v1/quality/checks/{id}` | quality:manage | Soft delete |
| POST | `/v1/quality/runs` | quality:manage | Trigger org-wide execution |
| GET | `/v1/quality/runs/{id}` | read | Run with per-check results |

## Lineage

| Method | Path | Description |
|---|---|---|
| POST | `/v1/lineage/openlineage` | Ingest one OpenLineage runtime event (idempotent) |
| POST | `/v1/lineage/dbt/manifest` | Ingest a dbt manifest.json |
| GET | `/v1/lineage/{assetID}/upstream?depth=N` | Upstream traversal (recursive CTE) |
| GET | `/v1/lineage/{assetID}/downstream?depth=N` | Downstream traversal |
| GET | `/v1/lineage/{assetID}/impact` | Impact analysis (datasets, jobs, checks at risk) |

## Observability

| Method | Path | Description |
|---|---|---|
| GET | `/v1/jobs` | Job registry with run statistics |
| GET | `/v1/jobs/{id}/runs` | Run history with error details |
| GET | `/v1/observability/health?days=N` | Success rate / duration aggregation |
| GET | `/v1/freshness` | Expected-vs-observed freshness per dataset |
| GET | `/v1/alerts?status=` | Alerts (OPEN/ACKNOWLEDGED/RESOLVED) |
| POST | `/v1/alerts/{id}/acknowledge` | Acknowledge |
| POST | `/v1/alerts/{id}/resolve` | Resolve with note |

## Governance

| Method | Path | Description |
|---|---|---|
| POST | `/v1/glossary/terms` | Create glossary term |
| GET | `/v1/glossary/terms` | List terms |
| POST | `/v1/glossary/terms/{id}/assets` | Attach term to asset |
| GET | `/v1/assets/{id}/terms` | Terms for an asset |
| POST | `/v1/assets/{id}/classifications` | Classify an asset |
| GET | `/v1/assets/{id}/classifications` | List asset classifications |
| POST | `/v1/catalog/columns/{id}/classifications` | Classify a column |
| GET | `/v1/audit-logs` | Recent audit entries |

## Permissions

Roles ordered by decreasing privilege: `OWNER > ADMIN > DATA_STEWARD >
DATA_ENGINEER > ANALYST / AUDITOR > MEMBER`.

Permission → minimum role: read=MEMBER, metadata:edit / ingestion:trigger /
sources:manage=DATA_ENGINEER, quality:manage=DATA_STEWARD,
api-keys:manage / members:manage=ADMIN, organization:manage=OWNER.
