# Connectors

DataAtlas ingests metadata through a uniform connector contract
(`app/connectors/base.py`): every connector declares a `connector_key` and
`platform`, validates connections, and reports discovery through
normalized models. Connectors are read-only by contract.

## Implemented connectors

### `postgres` — PostgreSQL metadata discovery

- Connection: config `host`, `port`, `database`, `user`; credentials carry
  the password (encrypted at rest in the control plane).
- **Session safety**: `default_transaction_read_only=on` and
  `statement_timeout` enforced server-side per session, regardless of the
  account's grants.
- Discovery: schemas, tables, views, materialized views; columns with
  types, nullability, defaults, primary keys, foreign keys (with referenced
  table/column resolution) and planner row estimates (`reltuples` — never a
  full scan).
- Scope limiting: set `schemas: ["schema_a", ...]` in the source config to
  restrict the metadata scan to an allowlist of schemas.
- Optional config `sslmode: require`.

### `csv_file` / `parquet_file` — file connectors

- Connection: config `path` pointing to a local `.csv`/`.parquet` file.
- Engine: DuckDB with bounded readers (`read_csv_auto` / `read_parquet` +
  `LIMIT`); files are never fully loaded into memory.
- Discovery: schema columns (DESCRIBE), exact row counts, file size,
  modified time and a content fingerprint.
- Validation errors (missing file, unsupported extension, missing config)
  surface as structured `ConnectorResult`s.

## Roadmap (not yet implemented — do not assume support)

Airflow, Dagster, Spark, Flink, Snowflake, BigQuery, Redshift, Databricks,
Iceberg, Delta Lake, Trino and Kafka connectors are design targets. See the
repository README for the implemented-and-tested list.

## Connector lifecycle

```
REGISTERED → CONFIGURED → VALIDATING → ACTIVE → INGESTING
                                   ↘ FAILED / DISCONNECTED / PAUSED
```

Transitions are recorded with reasons in `data_source_status_history`.
Connection tests and discovery jobs execute in Python workers over NATS:
the broker never carries secrets (workers load encrypted credentials from
PostgreSQL inside tenant-bound transactions and decrypt locally).
