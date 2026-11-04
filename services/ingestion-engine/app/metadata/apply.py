"""Catalog applier: normalizes and applies discovered metadata.

The applier is the metadata plane's write path into the catalog. It:
- upserts data assets and datasets,
- records immutable schema versions with checksums,
- computes change summaries and flags potentially breaking changes,
- merges column state while never overwriting user-authored descriptions,
- soft-deletes assets that disappeared from a full discovery.

All writes run inside transactions bound to the organization's RLS context.
"""

from __future__ import annotations

import hashlib
import json
import logging
from dataclasses import dataclass, field
from typing import Any

import asyncpg

from app.discovery.postgres_discovery import DiscoveredColumn, DiscoveredDataset

logger = logging.getLogger("dataatlas.applier")

ASSET_TYPE_BY_KEY = {"TABLE": "TABLE", "VIEW": "VIEW", "MATERIALIZED_VIEW": "MATERIALIZED_VIEW"}


@dataclass
class SchemaChange:
    added: list[str] = field(default_factory=list)
    removed: list[str] = field(default_factory=list)
    type_changed: dict[str, dict[str, str]] = field(default_factory=dict)
    nullability_changed: dict[str, str] = field(default_factory=dict)


@dataclass
class DatasetApplyResult:
    asset_id: str
    dataset_id: str
    schema_version: int
    changed: bool
    is_breaking: bool
    created: bool = False
    change: SchemaChange = field(default_factory=SchemaChange)


@dataclass
class ApplySummary:
    datasets_seen: int = 0
    datasets_created: int = 0
    schema_changes: int = 0
    breaking_changes: int = 0
    datasets_removed: int = 0
    results: list[DatasetApplyResult] = field(default_factory=list)

    def as_dict(self) -> dict[str, Any]:
        return {
            "datasets_seen": self.datasets_seen,
            "datasets_created": self.datasets_created,
            "schema_changes": self.schema_changes,
            "breaking_changes": self.breaking_changes,
            "datasets_removed": self.datasets_removed,
        }


def schema_checksum(columns: list[DiscoveredColumn]) -> str:
    """Stable checksum over the schema-defining attributes of columns."""
    payload = json.dumps(
        [
            [c.name, c.data_type, c.nullable, c.default or "", c.is_primary_key]
            for c in sorted(columns, key=lambda c: c.ordinal)
        ],
        separators=(",", ":"),
    )
    return hashlib.sha256(payload.encode()).hexdigest()


def compute_change(
    previous: list[dict[str, Any]], incoming: list[DiscoveredColumn]
) -> SchemaChange:
    """Diff the previous column state against the incoming discovery."""
    prev = {c["name"]: c for c in previous}
    change = SchemaChange()
    for col in incoming:
        old = prev.get(col.name)
        if old is None:
            change.added.append(col.name)
            continue
        if old["data_type"] != col.data_type:
            change.type_changed[col.name] = {
                "from": old["data_type"], "to": col.data_type,
            }
        if old["nullable"] != col.nullable:
            change.nullability_changed[col.name] = (
                "nullable -> not null" if not col.nullable else "not null -> nullable"
            )
    new_names = {c.name for c in incoming}
    change.removed = [name for name in prev if name not in new_names]
    return change


def is_breaking(change: SchemaChange) -> bool:
    """Conservative breaking-change rules: removals, type changes and
    tightened nullability are potentially breaking."""
    return bool(change.removed or change.type_changed or change.nullability_changed)


class CatalogApplier:
    """Applies discovery results to the catalog."""

    def __init__(self, pool: asyncpg.Pool) -> None:
        self._pool = pool

    async def apply_discovery(
        self,
        organization_id: str,
        source_id: str,
        source_name: str | None,
        datasets: list[DiscoveredDataset],
        fk_targets: dict[tuple[str, str, str], str],
        database_name: str | None = None,
    ) -> ApplySummary:
        """Apply every discovered dataset. Each dataset commits in its own
        transaction so one failure does not discard the whole run."""
        summary = ApplySummary(datasets_seen=len(datasets))
        seen_asset_ids: list[str] = []

        for ds in datasets:
            try:
                result = await self._apply_one(
                    organization_id, source_id, source_name, ds, fk_targets,
                    database_name,
                )
            except Exception:
                logger.exception("apply failed for %s.%s", ds.namespace, ds.name)
                raise
            seen_asset_ids.append(result.asset_id)
            if result.created:
                summary.datasets_created += 1
            if result.changed:
                summary.schema_changes += 1
                if result.is_breaking:
                    summary.breaking_changes += 1
            summary.results.append(result)

        removed = await self._mark_disappeared(
            organization_id, source_id, seen_asset_ids
        )
        summary.datasets_removed = removed
        return summary

    async def _apply_one(
        self,
        organization_id: str,
        source_id: str,
        source_name: str | None,
        ds: DiscoveredDataset,
        fk_targets: dict[tuple[str, str, str], str],
        database_name: str | None,
    ) -> DatasetApplyResult:
        async with self._pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )

                asset_id = await self._upsert_asset(conn, organization_id, source_id, ds)
                dataset_id = await self._upsert_dataset(
                    conn, organization_id, asset_id, ds, database_name
                )

                incoming_columns = []
                for col in ds.columns:
                    fk_ref = fk_targets.get((ds.namespace, ds.name, col.name))
                    incoming_columns.append(col)
                    if fk_ref and not col.is_foreign_key:
                        col.is_foreign_key = True
                        col.fk_reference = fk_ref
                    elif fk_ref:
                        col.fk_reference = fk_ref

                checksum = schema_checksum(ds.columns)
                previous = await self._previous_columns(conn, dataset_id)
                current_checksum = await conn.fetchval(
                    """
                    SELECT sv.checksum FROM datasets d
                    JOIN schema_versions sv ON sv.id = (
                        SELECT schema_version_id FROM dataset_versions
                        WHERE dataset_id = d.id
                        ORDER BY version_number DESC LIMIT 1
                    )
                    WHERE d.id = $1
                    """,
                    dataset_id,
                )

                if current_checksum == checksum:
                    await self._refresh_column_state(
                        conn, organization_id, dataset_id, ds, incoming_columns
                    )
                    return DatasetApplyResult(
                        asset_id=asset_id,
                        dataset_id=dataset_id,
                        schema_version=await self._latest_version(conn, dataset_id),
                        changed=False,
                        is_breaking=False,
                        created=False,
                    )

                change = compute_change(previous, incoming_columns)
                version_number = await self._latest_version(conn, dataset_id) + 1
                is_breaking_change = is_breaking(change)

                schema_version_id = await conn.fetchval(
                    """
                    INSERT INTO schema_versions
                        (dataset_id, organization_id, version_number, checksum,
                         change_summary, detected_by, is_breaking)
                    VALUES ($1, $2, $3, $4, $5, 'CONNECTOR', $6)
                    RETURNING id
                    """,
                    dataset_id,
                    organization_id,
                    version_number,
                    checksum,
                    json.dumps(change.__dict__, default=list),
                    is_breaking_change,
                )
                for col in sorted(ds.columns, key=lambda c: c.ordinal):
                    await conn.execute(
                        """
                        INSERT INTO schema_columns
                            (schema_version_id, organization_id, name, ordinal,
                             data_type, nullable, default_value, is_primary_key,
                             is_foreign_key, fk_reference, description)
                        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
                        """,
                        schema_version_id, organization_id, col.name, col.ordinal,
                        col.data_type, col.nullable, col.default,
                        col.is_primary_key, col.is_foreign_key, col.fk_reference,
                        col.native_description,
                    )  # native_description maps to schema_columns.description
                dataset_version_id = await conn.fetchval(
                    """
                    INSERT INTO dataset_versions
                        (dataset_id, organization_id, schema_version_id,
                         version_number, row_count, content_checksum, change_summary)
                    VALUES ($1, $2, $3, $4, $5, $6, $7)
                    RETURNING id
                    """,
                    dataset_id, organization_id, schema_version_id, version_number,
                    ds.row_estimate, checksum,
                    json.dumps(change.__dict__, default=list),
                )
                await conn.execute(
                    "UPDATE datasets SET current_version_id = $2, column_count = $3,"
                    " updated_at = now() WHERE id = $1",
                    dataset_id, dataset_version_id, len(ds.columns),
                )
                await self._merge_dataset_columns(
                    conn, organization_id, dataset_id, incoming_columns,
                    schema_version_id,
                )
                return DatasetApplyResult(
                    asset_id=asset_id,
                    dataset_id=dataset_id,
                    schema_version=version_number,
                    changed=True,
                    is_breaking=is_breaking_change,
                    created=current_checksum is None,
                    change=change,
                )

    async def _upsert_asset(
        self, conn: asyncpg.Connection, organization_id: str, source_id: str,
        ds: DiscoveredDataset,
    ) -> str:
        existing = await conn.fetchval(
            """
            SELECT id FROM data_assets
            WHERE organization_id = $1 AND data_source_id = $2
              AND namespace = $3 AND name = $4 AND asset_type = $5
              AND deleted_at IS NULL
            """,
            organization_id, source_id, ds.namespace, ds.name, ds.asset_type,
        )
        if existing:
            await conn.execute(
                "UPDATE data_assets SET last_seen_at = now(), updated_at = now() WHERE id = $1",
                existing,
            )
            return str(existing)
        native_description = (ds.native_metadata or {}).get("description")
        asset_id = await conn.fetchval(
            """
            INSERT INTO data_assets
                (organization_id, data_source_id, asset_type, namespace, name,
                 display_name, description, native_description, last_seen_at)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $7, now())
            RETURNING id
            """,
            organization_id, source_id, ds.asset_type, ds.namespace, ds.name,
            f"{ds.namespace}.{ds.name}", native_description,
        )
        return str(asset_id)

    async def _upsert_dataset(
        self, conn: asyncpg.Connection, organization_id: str, asset_id: str,
        ds: DiscoveredDataset, database_name: str | None,
    ) -> str:
        existing = await conn.fetchval(
            "SELECT id FROM datasets WHERE asset_id = $1 AND deleted_at IS NULL", asset_id,
        )
        if existing:
            await conn.execute(
                """
                UPDATE datasets SET row_count = $2, row_count_method = 'estimate',
                    schema_name = $3, updated_at = now()
                WHERE id = $1
                """,
                existing, ds.row_estimate, ds.namespace,
            )
            return str(existing)
        new_id = await conn.fetchval(
            """
            INSERT INTO datasets
                (asset_id, organization_id, row_count, row_count_method,
                 database_name, schema_name)
            VALUES ($1, $2, $3, 'estimate', $4, $5)
            RETURNING id
            """,
            asset_id, organization_id, ds.row_estimate, database_name, ds.namespace,
        )
        return str(new_id)

    async def _previous_columns(
        self, conn: asyncpg.Connection, dataset_id: str
    ) -> list[dict[str, Any]]:
        rows = await conn.fetch(
            """
            SELECT name, data_type, nullable FROM dataset_columns
            WHERE dataset_id = $1 AND status = 'ACTIVE'
            """,
            dataset_id,
        )
        return [dict(r) for r in rows]

    async def _latest_version(self, conn: asyncpg.Connection, dataset_id: str) -> int:
        value = await conn.fetchval(
            """
            SELECT MAX(v.version_number) FROM dataset_versions v
            WHERE v.dataset_id = $1
            """,
            dataset_id,
        )
        return value or 0

    async def _refresh_column_state(
        self, conn: asyncpg.Connection, organization_id: str, dataset_id: str,
        ds: DiscoveredDataset, incoming_columns: list[DiscoveredColumn],
    ) -> None:
        """Checksum unchanged: only refresh native metadata and last_seen."""
        await conn.execute(
            """
            UPDATE dataset_columns SET status = 'ACTIVE', updated_at = now()
            WHERE dataset_id = $1 AND name = ANY($2)
            """,
            dataset_id, [c.name for c in incoming_columns],
        )

    async def _merge_dataset_columns(
        self, conn: asyncpg.Connection, organization_id: str, dataset_id: str,
        incoming: list[DiscoveredColumn], schema_version_id: str,
    ) -> None:
        """Merge the incoming schema into dataset_columns.

        Merge policy: user-authored descriptions are never overwritten;
        native descriptions only fill gaps. Columns absent from the incoming
        snapshot are marked REMOVED.
        """
        incoming_names = {c.name for c in incoming}
        await conn.execute(
            """
            UPDATE dataset_columns SET status = 'REMOVED', updated_at = now()
            WHERE dataset_id = $1 AND status = 'ACTIVE' AND NOT (name = ANY($2))
            """,
            dataset_id, list(incoming_names),
        )
        for col in incoming:
            await conn.execute(
                """
                INSERT INTO dataset_columns
                    (dataset_id, organization_id, name, ordinal, data_type,
                     nullable, default_value, is_primary_key, is_foreign_key,
                     fk_reference, native_description, first_seen_version,
                     last_seen_version, status)
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12,
                        'ACTIVE')
                ON CONFLICT (dataset_id, name) WHERE status = 'ACTIVE' DO UPDATE
                SET ordinal = EXCLUDED.ordinal,
                    data_type = EXCLUDED.data_type,
                    nullable = EXCLUDED.nullable,
                    default_value = EXCLUDED.default_value,
                    is_primary_key = EXCLUDED.is_primary_key,
                    is_foreign_key = EXCLUDED.is_foreign_key,
                    fk_reference = EXCLUDED.fk_reference,
                    native_description = COALESCE(dataset_columns.native_description,
                                  EXCLUDED.native_description),
                    last_seen_version = EXCLUDED.last_seen_version,
                    updated_at = now()
                """,
                dataset_id, organization_id, col.name, col.ordinal, col.data_type,
                col.nullable, col.default, col.is_primary_key, col.is_foreign_key,
                col.fk_reference, col.native_description, schema_version_id,
            )

    async def _mark_disappeared(
        self, organization_id: str, source_id: str, seen_asset_ids: list[str]
    ) -> int:
        """Soft-delete this source's assets absent from a full discovery."""
        async with self._pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                rows = await conn.fetch(
                    """
                    SELECT id FROM data_assets
                    WHERE organization_id = $1 AND data_source_id = $2
                      AND deleted_at IS NULL
                    """,
                    organization_id, source_id,
                )
                seen = set(seen_asset_ids)
                removed = 0
                for row in rows:
                    if str(row["id"]) in seen:
                        continue
                    await conn.execute(
                        """
                        UPDATE data_assets SET deleted_at = now(), updated_at = now()
                        WHERE id = $1
                        """,
                        row["id"],
                    )
                    await conn.execute(
                        """
                        UPDATE dataset_columns SET status = 'REMOVED', updated_at = now()
                        WHERE dataset_id IN (SELECT id FROM datasets WHERE asset_id = $1)
                        """,
                        row["id"],
                    )
                    removed += 1
                return removed
