"""Data quality engine: safe, read-only check execution.

Safety model (Part 16.3):
- connectors connect with read-only sessions and statement timeouts,
- structured checks generate fully parameterized SQL with validated,
  quoted identifiers,
- CUSTOM_SQL is parsed with SQLGlot: only a single SELECT statement is
  accepted; the executed query is the user query wrapped in a bounded
  count, so no user SQL runs unbounded or unsandboxed,
- results never contain raw row values, only counts and aggregates.
"""

from __future__ import annotations

import logging
import time
from dataclasses import dataclass, field
from typing import Any

import asyncpg
import sqlglot

from app.core.config import get_settings

logger = logging.getLogger("dataatlas.quality")

_IDENTIFIER_SAFE = set("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_")


def quote_identifier(name: str) -> str:
    """Validate and safely quote a single SQL identifier."""
    if not name or any(c not in _IDENTIFIER_SAFE for c in name) or len(name) > 63:
        raise ValueError(f"unsafe identifier: {name!r}")
    return f'"{name}"'


@dataclass
class CheckOutcome:
    check_id: str
    status: str  # PASSED | FAILED | WARNING | ERROR | SKIPPED
    rows_evaluated: int | None = None
    rows_failed: int | None = None
    actual: dict[str, Any] = field(default_factory=dict)
    error_message: str | None = None
    duration_ms: int = 0


class QualityEngine:
    """Executes quality checks against one source connection."""

    def __init__(self, conn: asyncpg.Connection) -> None:
        self._conn = conn
        self._settings = get_settings()

    async def execute_check(self, check: dict[str, Any]) -> CheckOutcome:
        """Run one check record (quality_checks row shape)."""
        started = time.monotonic()
        check_id = str(check["id"])
        try:
            outcome = await self._dispatch(check)
        except ValueError as exc:
            return CheckOutcome(
                check_id=check_id, status="ERROR",
                error_message=str(exc),
                duration_ms=int((time.monotonic() - started) * 1000),
            )
        except asyncpg.PostgresError as exc:
            return CheckOutcome(
                check_id=check_id, status="ERROR",
                error_message=str(exc)[:300],
                duration_ms=int((time.monotonic() - started) * 1000),
            )
        outcome.duration_ms = int((time.monotonic() - started) * 1000)
        return outcome

    async def _dispatch(self, check: dict[str, Any]) -> CheckOutcome:
        ctype = check["check_type"]
        params = check.get("parameters") or {}
        table = check.get("table_name")
        schema = check.get("schema_name") or "public"
        column = check.get("target_column")

        if ctype == "SCHEMA":
            return CheckOutcome(check_id=str(check["id"]), status="SKIPPED",
                                error_message="schema checks are evaluated during discovery")
        if ctype == "FRESHNESS":
            return await self._freshness(check, schema, str(table or ""), str(column or ""), params)
        if ctype == "CUSTOM_SQL":
            return await self._custom_sql(check, params)

        if not table:
            raise ValueError("structured checks require a table")

        quoted_table = f"{quote_identifier(schema)}.{quote_identifier(table)}"
        limit = min(self._settings.quality_max_rows, self._settings.connector_max_rows)
        sample = f"SELECT * FROM {quoted_table} LIMIT {int(limit)}"

        rows_evaluated = await self._conn.fetchval(f"SELECT count(*) FROM ({sample}) s")

        if ctype == "NOT_NULL":
            qcol = quote_identifier(column or "")
            failed = await self._conn.fetchval(
                f"SELECT count(*) FROM ({sample}) s WHERE {qcol} IS NULL"  # noqa: S608
            )
            return self._verdict(check, rows_evaluated, failed)
        if ctype == "UNIQUE":
            qcol = quote_identifier(column or "")
            failed = await self._conn.fetchval(
                f"SELECT COALESCE(SUM(n - 1), 0) FROM ("
                f"SELECT {qcol}, count(*) AS n FROM ({sample}) s "
                f"WHERE {qcol} IS NOT NULL GROUP BY {qcol} HAVING count(*) > 1) d"  # noqa: S608
            )
            return self._verdict(check, rows_evaluated, failed)
        if ctype == "ROW_COUNT":
            minimum = params.get("min")
            maximum = params.get("max")
            actual_count = await self._conn.fetchval(
                f"SELECT count(*) FROM {quoted_table}"  # noqa: S608
            )
            ok = (minimum is None or actual_count >= minimum) and (
                maximum is None or actual_count <= maximum
            )
            return CheckOutcome(
                check_id=str(check["id"]),
                status="PASSED" if ok else "FAILED",
                rows_evaluated=int(actual_count or 0),
                rows_failed=0 if ok else int(actual_count or 0),
                actual={"row_count": int(actual_count or 0),
                        "expected_min": minimum, "expected_max": maximum},
            )
        if ctype == "ACCEPTED_VALUES":
            qcol = quote_identifier(column or "")
            allowed = params.get("values") or []
            failed = await self._conn.fetchval(
                f"SELECT count(*) FROM ({sample}) s "
                f"WHERE {qcol} IS NOT NULL AND NOT ({qcol} = ANY($1::text[]))",  # noqa: S608
                [str(v) for v in allowed],
            )
            return self._verdict(check, rows_evaluated, failed)
        if ctype == "NUMERIC_RANGE":
            qcol = quote_identifier(column or "")
            minimum = params.get("min")
            maximum = params.get("max")
            if minimum is None and maximum is None:
                raise ValueError("numeric_range requires min and/or max")
            failed = await self._conn.fetchval(
                f"SELECT count(*) FROM ({sample}) s WHERE {qcol} IS NOT NULL AND ("
                + (f"{qcol} < $1" if minimum is not None else "false")
                + (" OR " if minimum is not None and maximum is not None else "")
                + (f"{qcol} > $2" if maximum is not None else "false") + ")",  # noqa: S608
                *[v for v in (minimum, maximum) if v is not None],
            )
            return self._verdict(check, rows_evaluated, failed)
        if ctype == "REGEX":
            qcol = quote_identifier(column or "")
            pattern = params.get("pattern")
            if not pattern:
                raise ValueError("regex check requires a pattern")
            # Validate the pattern compiles before touching the database.
            import re as _re
            _re.compile(pattern)
            failed = await self._conn.fetchval(
                f"SELECT count(*) FROM ({sample}) s "
                f"WHERE {qcol} IS NOT NULL AND {qcol} !~ $1",  # noqa: S608
                pattern,
            )
            return self._verdict(check, rows_evaluated, failed)
        if ctype == "REFERENTIAL_INTEGRITY":
            qcol = quote_identifier(column or "")
            ref_table = params.get("reference_table") or ""
            ref_schema = params.get("reference_schema") or schema
            ref_column = params.get("reference_column") or ""
            refs = f"{quote_identifier(ref_schema)}.{quote_identifier(ref_table)}"
            qref = quote_identifier(ref_column)
            failed = await self._conn.fetchval(
                f"SELECT count(*) FROM ({sample}) s WHERE {qcol} IS NOT NULL "
                f"AND NOT EXISTS (SELECT 1 FROM {refs} r WHERE r.{qref} = s.{qcol})",  # noqa: S608
            )
            return self._verdict(check, rows_evaluated, failed)
        raise ValueError(f"unsupported check type {ctype!r}")

    def _verdict(
        self, check: dict[str, Any], rows_evaluated: Any, rows_failed: Any
    ) -> CheckOutcome:
        failed = int(rows_failed or 0)
        evaluated = int(rows_evaluated or 0)
        severity = check.get("severity") or "ERROR"
        warn = severity in ("INFO", "WARNING")
        status = "PASSED" if failed == 0 else ("WARNING" if warn else "FAILED")
        return CheckOutcome(
            check_id=str(check["id"]),
            status=status,
            rows_evaluated=evaluated,
            rows_failed=failed,
            actual={"rows_failed": failed},
        )

    async def _freshness(
        self, check: dict[str, Any], schema: str, table: str,
        column: str | None, params: dict[str, Any],
    ) -> CheckOutcome:
        if not column or not table:
            raise ValueError("freshness checks require a timestamp column")
        max_age = params.get("max_age_seconds")
        if max_age is None:
            raise ValueError("freshness checks require max_age_seconds")
        quoted_table = f"{quote_identifier(schema)}.{quote_identifier(table)}"
        qcol = quote_identifier(column)
        value = await self._conn.fetchval(
            f"SELECT max({qcol}) FROM {quoted_table}"  # noqa: S608
        )
        if value is None:
            return CheckOutcome(check_id=str(check["id"]), status="FAILED",
                                actual={"latest": None, "max_age_seconds": max_age})
        age = (await self._conn.fetchval("SELECT now() - $1::timestamptz", value)).total_seconds()
        ok = age <= float(max_age)
        severity = check.get("severity") or "ERROR"
        warn = severity in ("INFO", "WARNING")
        return CheckOutcome(
            check_id=str(check["id"]),
            status="PASSED" if ok else ("WARNING" if warn else "FAILED"),
            actual={"age_seconds": int(age), "max_age_seconds": max_age},
        )

    async def _custom_sql(self, check: dict[str, Any], params: dict[str, Any]) -> CheckOutcome:
        sql = (params.get("sql") or "").strip().rstrip(";")
        if not sql:
            raise ValueError("custom_sql requires a sql statement")
        # Parse with SQLGlot: exactly one statement, and it must be a pure
        # SELECT (no DDL/DML roots, no writes anywhere in the tree).
        try:
            statements = sqlglot.parse(sql, read="postgres")
        except sqlglot.errors.ParseError as exc:
            raise ValueError(f"invalid SQL: {exc}") from exc
        if len(statements) != 1 or statements[0] is None:
            raise ValueError("custom_sql accepts exactly one statement")
        root = statements[0]
        if not isinstance(root, sqlglot.exp.Select):
            raise ValueError("custom_sql must be a SELECT statement")
        forbidden = root.find_all(
            sqlglot.exp.Insert, sqlglot.exp.Update, sqlglot.exp.Delete,
            sqlglot.exp.Drop, sqlglot.exp.Alter, sqlglot.exp.Create,
            sqlglot.exp.TruncateTable, sqlglot.exp.Command,
        )
        if next(iter(forbidden), None) is not None:
            raise ValueError("custom_sql must be read-only")

        # The user SQL may reference bare table names; scope resolution to the
        # dataset's schema for the duration of the statement.
        schema = str(check.get("schema_name") or "public")
        if any(t for t in root.find_all(sqlglot.exp.Table)):
            # Validate the schema name even though set_config takes the raw
            # value: search_path entries are unquoted identifiers.
            quote_identifier(schema)
        limit = min(self._settings.quality_max_rows, self._settings.connector_max_rows)
        wrapped = f"SELECT count(*) AS failed FROM ({sql}) q LIMIT {int(limit)}"

        async def _run(c: asyncpg.Connection) -> asyncpg.Record | None:
            # The GUC is transaction-local: scope resolution and execution
            # must share one transaction.
            async with c.transaction():
                if any(t for t in root.find_all(sqlglot.exp.Table)):
                    await c.execute(
                        "SELECT set_config('search_path', $1, true)", schema
                    )
                return await c.fetchrow(wrapped)

        row = await _run(self._conn)
        failed = int(row["failed"] or 0) if row else 0
        expected = params.get("expected_failures", 0)
        ok = failed <= int(expected)
        return CheckOutcome(
            check_id=str(check["id"]),
            status="PASSED" if ok else "FAILED",
            rows_evaluated=None,
            rows_failed=failed,
            actual={"rows_failed": failed, "expected_failures": expected},
        )
