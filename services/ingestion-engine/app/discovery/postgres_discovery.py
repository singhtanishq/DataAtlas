"""PostgreSQL full metadata discovery.

Extracts schemas, tables, views, columns, primary and foreign keys, and row
count estimates using only catalog queries — never table data. All queries
run with statement timeouts on the read-only session established by the
connector.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

import asyncpg

# Schemas that are implementation noise, never business metadata.
EXCLUDED_SCHEMAS = {"pg_catalog", "information_schema", "pg_toast"}


@dataclass
class DiscoveredColumn:
    name: str
    ordinal: int
    data_type: str
    nullable: bool
    default: str | None = None
    is_primary_key: bool = False
    is_foreign_key: bool = False
    fk_reference: str | None = None
    native_description: str | None = None


@dataclass
class DiscoveredDataset:
    """One table or view with its columns and row estimate."""

    namespace: str  # schema name
    name: str  # table name
    asset_type: str  # TABLE | VIEW | MATERIALIZED_VIEW
    row_estimate: int | None = None
    columns: list[DiscoveredColumn] = field(default_factory=list)
    native_metadata: dict[str, Any] = field(default_factory=dict)


class PostgreSQLDiscovery:
    """Catalog-only discovery for one PostgreSQL database.

    ``allowed_schemas`` restricts the scan scope when configured on the
    source; None means every non-internal schema.
    """

    def __init__(
        self, connection_kwargs: dict[str, Any], allowed_schemas: list[str] | None = None
    ) -> None:
        self._conn_kwargs = connection_kwargs
        self._allowed_schemas = allowed_schemas

    async def discover_schemas(self) -> list[str]:
        conn = await asyncpg.connect(**self._conn_kwargs)
        try:
            rows = await conn.fetch(
                """
                SELECT nspname FROM pg_namespace
                WHERE nspname NOT IN ('pg_catalog', 'information_schema')
                  AND nspname !~ '^pg_toast'
                ORDER BY nspname
                """
            )
            names = [r["nspname"] for r in rows]
            if self._allowed_schemas is not None:
                allowed = set(self._allowed_schemas)
                names = [n for n in names if n in allowed]
            return names
        finally:
            await conn.close()

    async def discover_datasets(self) -> list[DiscoveredDataset]:
        """Discover tables and views with row estimates in one query."""
        conn = await asyncpg.connect(**self._conn_kwargs)
        try:
            rows = await conn.fetch(
                """
                SELECT n.nspname AS schema_name,
                       c.relname AS table_name,
                       CASE c.relkind
                           WHEN 'r' THEN 'TABLE'
                           WHEN 'p' THEN 'TABLE'
                           WHEN 'v' THEN 'VIEW'
                           WHEN 'm' THEN 'MATERIALIZED_VIEW'
                       END AS asset_type,
                       c.reltuples::bigint AS row_estimate,
                       obj_description(c.oid, 'pg_class') AS description
                FROM pg_class c
                JOIN pg_namespace n ON n.oid = c.relnamespace
                WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
                  AND n.nspname !~ '^pg_toast'
                  AND c.relkind IN ('r', 'p', 'v', 'm')
                ORDER BY n.nspname, c.relname
                """
            )
            datasets = [
                DiscoveredDataset(
                    namespace=r["schema_name"],
                    name=r["table_name"],
                    asset_type=r["asset_type"],
                    # reltuples is a planner estimate; -1 means never analyzed.
                    row_estimate=max(r["row_estimate"], 0) if r["row_estimate"] >= 0 else None,
                    native_metadata={"description": r["description"]},
                )
                for r in rows
            ]
            if self._allowed_schemas is not None:
                allowed = set(self._allowed_schemas)
                datasets = [d for d in datasets if d.namespace in allowed]
            return datasets
        finally:
            await conn.close()

    async def discover_columns(self, schema: str, table: str) -> list[DiscoveredColumn]:
        conn = await asyncpg.connect(**self._conn_kwargs)
        try:
            rows = await conn.fetch(
                """
                SELECT a.attname AS column_name,
                       a.attnum AS ordinal,
                       pg_catalog.format_type(a.atttypid, a.atttypmod) AS data_type,
                       NOT a.attnotnull AS nullable,
                       pg_get_expr(ad.adbin, ad.adrelid) AS default_value,
                       EXISTS (
                           SELECT 1 FROM pg_constraint pk
                           WHERE pk.conrelid = a.attrelid AND pk.contype = 'p'
                             AND a.attnum = ANY(pk.conkey)
                       ) AS is_primary_key,
                       EXISTS (
                           SELECT 1 FROM pg_constraint fk
                           WHERE fk.conrelid = a.attrelid AND fk.contype = 'f'
                             AND a.attnum = ANY(fk.conkey)
                       ) AS is_foreign_key,
                       col_description(a.attrelid, a.attnum) AS description
                FROM pg_attribute a
                JOIN pg_class c ON c.oid = a.attrelid
                JOIN pg_namespace n ON n.oid = c.relnamespace
                LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
                WHERE n.nspname = $1 AND c.relname = $2
                  AND a.attnum > 0 AND NOT a.attisdropped
                ORDER BY a.attnum
                """,
                schema,
                table,
            )
            columns = [DiscoveredColumn(
                name=r["column_name"],
                ordinal=r["ordinal"],
                data_type=r["data_type"],
                nullable=r["nullable"],
                default=r["default_value"],
                is_primary_key=r["is_primary_key"],
                is_foreign_key=r["is_foreign_key"],
                native_description=r["description"],
            ) for r in rows]
            return columns
        finally:
            await conn.close()

    async def foreign_key_targets(self) -> dict[tuple[str, str, str], str]:
        """Map (schema, table, column) -> referenced 'schema.table.column'."""
        conn = await asyncpg.connect(**self._conn_kwargs)
        try:
            rows = await conn.fetch(
                """
                SELECT ns.nspname AS schema_name, tc.relname AS table_name,
                       a.attname AS column_name,
                       rns.nspname AS ref_schema, rtc.relname AS ref_table,
                       ra.attname AS ref_column
                FROM pg_constraint con
                JOIN pg_class tc ON tc.oid = con.conrelid
                JOIN pg_namespace ns ON ns.oid = tc.relnamespace
                JOIN pg_class rtc ON rtc.oid = con.confrelid
                JOIN pg_namespace rns ON rns.oid = rtc.relnamespace
                JOIN LATERAL unnest(con.conkey, con.confkey) AS ck(attnum, refattnum) ON true
                JOIN pg_attribute a ON a.attrelid = tc.oid AND a.attnum = ck.attnum
                JOIN pg_attribute ra ON ra.attrelid = rtc.oid AND ra.attnum = ck.refattnum
                WHERE con.contype = 'f'
                  AND ns.nspname NOT IN ('pg_catalog', 'information_schema')
                """
            )
            return {
                (r["schema_name"], r["table_name"], r["column_name"]):
                    f"{r['ref_schema']}.{r['ref_table']}.{r['ref_column']}"
                for r in rows
            }
        finally:
            await conn.close()
