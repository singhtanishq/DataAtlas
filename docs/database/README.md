# Database Schema

PostgreSQL is the canonical system of record. 45+ tables across ten
migrations, all organization-scoped and protected by Row-Level Security.

## Core relationships

```mermaid
erDiagram
    organizations ||--o{ memberships : has
    users ||--o{ memberships : holds
    organizations ||--o{ teams : has
    teams ||--o{ team_members : contains
    organizations ||--o{ data_sources : registers
    data_sources ||--|| source_credentials : "encrypted with"
    data_sources ||--o{ ingestion_pipelines : defines
    ingestion_pipelines ||--o{ ingestion_runs : executes
    ingestion_pipelines ||--o{ connector_checkpoints : resumes
    organizations ||--o{ data_assets : catalogs
    data_sources ||--o{ data_assets : "discovered from"
    data_assets ||--|| datasets : "extended by"
    datasets ||--o{ dataset_versions : versions
    dataset_versions }o--|| schema_versions : "pins snapshot"
    schema_versions ||--o{ schema_columns : "immutable columns"
    datasets ||--o{ dataset_columns : "current state"
    datasets ||--o{ profiling_runs : profiles
    profiling_runs ||--o{ profile_snapshots : stores
    profile_snapshots ||--o{ column_profiles : metrics
    datasets ||--o{ quality_checks : checks
    quality_checks ||--o{ quality_results : results
    quality_results }o--|| quality_runs : "part of"
    datasets ||--o{ freshness_records : expectations
    organizations ||--o{ jobs : registers
    jobs ||--o{ job_runs : executes
    jobs }o--o{ datasets : "job_inputs / job_outputs"
    data_assets ||--o{ lineage_edges : "source-target"
    lineage_edges ||--o{ column_lineage_edges : "column detail"
    organizations ||--o{ business_glossaries : defines
    business_glossaries ||--o{ glossary_terms : contains
    glossary_terms }o--o{ data_assets : "glossary_term_assets"
    data_assets ||--o{ asset_tags : tagged
    data_assets ||--o{ ownership_assignments : owned
    data_assets ||--o{ asset_classifications : classified
    dataset_columns ||--o{ column_classifications : classified
    organizations ||--o{ alerts : raises
    organizations ||--o{ audit_logs : records
    organizations ||--o{ api_keys : issues
```

## Identity and versioning

- **Canonical asset identity**: unique per
  `(organization_id, data_source_id, namespace, name, asset_type)` with a
  generated `full_name` (`namespace.name`) — never mutable display names.
- **Version chain**: `datasets.current_version_id → dataset_versions →
  schema_versions` (immutable snapshots with content checksums).
- **Run records are immutable** once finished; progress payloads are JSONB.

## Row-Level Security

- All organization-scoped tables carry `organization_id` and are
  `FORCE ROW LEVEL SECURITY`.
- Policies compare against `current_setting('app.current_organization_id')`
  (NULLIF-guarded: unset GUC → zero rows).
- `job_inputs`, `job_outputs` and `team_members` derive isolation through
  parent joins.
- The application role `dataatlas_app` is NOSUPERUSER / NOBYPASSRLS so
  policies always apply; see `docs/security/threat-model.md`.

## Migrations

Plain SQL pairs `NNN_name.up.sql` / `.down.sql` under
`services/control-plane/migrations/`, applied transactionally by the
embedded runner (`go run ./cmd/migrate up|down|status`). Migrations are
per-database self-contained — they never perform cluster-wide role
operations in down scripts.
