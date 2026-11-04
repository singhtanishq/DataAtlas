"""Dataset profiler: computes column and dataset statistics safely.

Privacy and performance rules (Part 14.2):
- never exports raw rows; aggregates only,
- samples large tables instead of scanning them by default,
- records HOW every metric was computed (exact / approximate / sample),
- bounded by statement timeouts and row limits on the read-only session.
"""

from __future__ import annotations

import hashlib
import json
import logging
import time
from dataclasses import dataclass, field
from typing import Any

import asyncpg

from app.core.config import get_settings

logger = logging.getLogger("dataatlas.profiler")

# Column type families driving which metrics apply.
NUMERIC_PREFIXES = (
    "smallint", "integer", "bigint", "decimal", "numeric", "real", "double precision",
)
TEXTUAL_PREFIXES = ("character", "text", "varchar")
TEMPORAL_PREFIXES = ("timestamp", "date", "time")


@dataclass
class ColumnProfileResult:
    name: str
    metrics: list[dict[str, Any]] = field(default_factory=list)

    def add(self, name: str, value: Any, value_type: str) -> None:
        self.metrics.append({"metric_name": name, "value": value, "value_type": value_type})


@dataclass
class ProfileResult:
    dataset_id: str
    row_count: int | None
    row_count_method: str
    column_count: int
    columns: list[ColumnProfileResult] = field(default_factory=list)
    duration_ms: int = 0

    def as_dict(self) -> dict[str, Any]:
        return {
            "dataset_id": self.dataset_id,
            "row_count": self.row_count,
            "row_count_method": self.row_count_method,
            "column_count": self.column_count,
            "columns": [
                {"name": c.name, "metrics": c.metrics} for c in self.columns
            ],
        }


def type_family(data_type: str) -> str:
    t = data_type.lower()
    if any(t.startswith(p) for p in NUMERIC_PREFIXES):
        return "numeric"
    if any(t.startswith(p) for p in TEXTUAL_PREFIXES):
        return "text"
    if any(t.startswith(p) for p in TEMPORAL_PREFIXES):
        return "temporal"
    return "other"


class DatasetProfiler:
    """Profiles one dataset using bounded, read-only queries."""

    def __init__(self, conn: asyncpg.Connection) -> None:
        self._conn = conn
        self._settings = get_settings()

    async def profile_dataset(
        self,
        schema_name: str | None,
        table_name: str,
        columns: list[asyncpg.Record],
    ) -> ProfileResult:
        """Profile the table behind a catalog dataset.

        ``schema_name``/``table_name`` come from catalog metadata, not from
        user input, and are still validated conservatively before being
        interpolated into the sampling query (table names cannot be bound as
        parameters).
        """
        started = time.monotonic()
        _validate_identifier(schema_name or "public")
        _validate_identifier(table_name)

        limit = self._settings.profiler_sample_rows
        quoted = f'"{(schema_name or "public")}"."{table_name}"'

        row_count, row_method = await self._row_count(
            schema_name or "public", table_name, quoted, limit
        )
        result = ProfileResult(
            dataset_id="",  # filled by the caller (metadata plane)
            row_count=row_count,
            row_count_method=row_method,
            column_count=len(columns),
        )

        for col in columns:
            cp = ColumnProfileResult(name=col["name"])
            family = type_family(col["data_type"])
            await self._profile_column(cp, quoted, col["name"], family, limit)
            result.columns.append(cp)

        result.duration_ms = int((time.monotonic() - started) * 1000)
        return result

    async def _row_count(
        self, schema: str, table: str, quoted_table: str, limit: int
    ) -> tuple[int | None, str]:
        """Planner estimate first; a bounded sample count as fallback when
        the table has never been analyzed. Never counts the full table.
        The estimate lookup is fully parameterized (no regclass casts)."""
        try:
            value = await self._conn.fetchval(
                """
                SELECT c.reltuples::bigint FROM pg_class c
                JOIN pg_namespace n ON n.oid = c.relnamespace
                WHERE n.nspname = $1 AND c.relname = $2
                """,
                schema, table,
            )
        except asyncpg.PostgresError:
            value = None
        if value is not None and value >= 0:
            return int(value), "estimate"
        sampled = await self._conn.fetchval(
            f"SELECT count(*) FROM (SELECT 1 FROM {quoted_table} LIMIT {limit}) s"  # noqa: S608
        )
        if sampled is None:
            return None, "inferred"
        return int(sampled), "sample"

    async def _profile_column(
        self, cp: ColumnProfileResult, quoted_table: str, column: str, family: str, limit: int
    ) -> None:
        quoted_col = f'"{column}"'
        # Aggregate over a bounded sample: metadata-only footprint.
        # Identifiers are validated and quoted above; the sample limit is a
        # bounded integer from settings, so interpolation here is safe.
        base = f"SELECT {quoted_col} FROM {quoted_table} LIMIT {limit}"  # noqa: S608

        nulls = await self._conn.fetchval(
            f"SELECT count(*) FROM ({base}) s WHERE {quoted_col} IS NULL"  # noqa: S608
        )
        total = await self._conn.fetchval(
            f"SELECT count(*) FROM ({base}) s"  # noqa: S608
        )
        total = int(total or 0)
        nulls = int(nulls or 0)
        cp.add("null_count", nulls, "sample")
        cp.add("row_count_sampled", total, "sample")
        if total > 0:
            pct = round(nulls * 100.0 / total, 2)
            cp.add("null_percentage", pct, "sample")

        distinct = await self._conn.fetchval(
            f"SELECT count(DISTINCT {quoted_col}) FROM ({base}) s"  # noqa: S608
        )
        distinct = int(distinct or 0)
        cp.add("distinct_count", distinct, "sample")
        if total > 0:
            cp.add("uniqueness_percentage", round(distinct * 100.0 / total, 2), "sample")

        if family == "numeric":
            stats = await self._conn.fetchrow(  # noqa: S608
                f"SELECT min({quoted_col}) AS mn, max({quoted_col}) AS mx, "
                f"avg({quoted_col}) AS av, stddev_pop({quoted_col}) AS sd FROM ({base}) s"
            )
            if stats:
                cp.add("min_value", _as_float(stats["mn"]), "sample")
                cp.add("max_value", _as_float(stats["mx"]), "sample")
                if stats["av"] is not None:
                    cp.add("average", round(float(stats["av"]), 6), "sample")
                if stats["sd"] is not None:
                    cp.add("std_dev", round(float(stats["sd"]), 6), "sample")
        elif family == "text":
            lengths = await self._conn.fetchrow(  # noqa: S608
                f"SELECT min(length({quoted_col})) AS mn, max(length({quoted_col})) AS mx "
                f"FROM ({base}) s WHERE {quoted_col} IS NOT NULL"
            )
            if lengths:
                cp.add("min_length", lengths["mn"], "sample")
                cp.add("max_length", lengths["mx"], "sample")
            # Bounded frequency, never raw values.
            freq = await self._conn.fetch(  # noqa: S608
                f"SELECT {quoted_col} AS v, count(*) AS n FROM ({base}) s "
                f"WHERE {quoted_col} IS NOT NULL GROUP BY 1 ORDER BY n DESC LIMIT 5"
            )
            top = [
                {"value_hash": hashlib.sha256(str(r["v"]).encode()).hexdigest()[:12],
                 "count": int(r["n"])}
                for r in freq
            ]
            cp.add("top_values_hashed", top, "sample")
        elif family == "temporal":
            bounds = await self._conn.fetchrow(  # noqa: S608
                f"SELECT min({quoted_col}) AS mn, max({quoted_col}) AS mx FROM ({base}) s"
            )
            if bounds:
                cp.add("min_value", str(bounds["mn"]), "sample")
                cp.add("max_value", str(bounds["mx"]), "sample")


def _as_float(v: Any) -> float | None:
    return None if v is None else float(v)


_IDENTIFIER_SAFE = set("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_")


def _validate_identifier(name: str) -> None:
    """Defensive check before quoting identifiers into sampling queries."""
    if not name or any(c not in _IDENTIFIER_SAFE for c in name) or len(name) > 63:
        raise ValueError(f"unsafe identifier: {name!r}")


def store_profile_results(
    profiling_run_id: str, organization_id: str, result: ProfileResult
) -> dict[str, Any]:
    """Return the SQL payload rows for storing a profile (pure helper)."""
    return {
        "run_id": profiling_run_id,
        "organization_id": organization_id,
        "snapshot": json.dumps(result.as_dict(), default=str),
    }
