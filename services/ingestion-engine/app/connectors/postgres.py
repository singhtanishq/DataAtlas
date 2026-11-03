"""PostgreSQL metadata connector.

Connects with a dedicated read-only account, applies connection and
statement timeouts, and never issues write statements.
"""

from __future__ import annotations

import time
from typing import Any, ClassVar

import asyncpg

from app.connectors.base import ConnectorError, ConnectorResult, DataConnector, connector_settings


class PostgreSQLConnector(DataConnector):
    """Discovery and profiling connector for PostgreSQL databases."""

    connector_key: ClassVar[str] = "postgres"
    platform: ClassVar[str] = "postgresql"

    REQUIRED_CONFIG = ("host", "database")

    def _connection_kwargs(self) -> dict[str, Any]:
        limits = connector_settings()
        missing = [k for k in self.REQUIRED_CONFIG if not self.config.get(k)]
        if missing:
            raise ConnectorError(f"missing required configuration: {', '.join(missing)}")
        if not self.credentials.get("password"):
            raise ConnectorError("missing credential: password")
        return {
            "host": str(self.config["host"]),
            "port": int(self.config.get("port", 5432)),
            "database": str(self.config["database"]),
            "user": str(self.config.get("user", "dataatlas_scanner")),
            "password": str(self.credentials["password"]),
            "timeout": float(limits["connect_timeout"]),
            "server_settings": {
                # Defense-in-depth: even if the account is not read-only,
                # sessions default to read-only transactions with timeouts.
                "default_transaction_read_only": "on",
                "statement_timeout": str(int(limits["query_timeout"] * 1000)),
            },
            "ssl": (
                "require"
                if str(self.config.get("sslmode", "prefer")) == "require"
                else "prefer"
            ),
        }

    async def validate_connection(self) -> ConnectorResult:
        """Connect, verify read-only session defaults, and close."""
        start = time.monotonic()
        conn: asyncpg.Connection | None = None
        try:
            conn = await asyncpg.connect(**self._connection_kwargs())
            version = await conn.fetchval("SELECT version()")
            read_only = await conn.fetchval("SHOW default_transaction_read_only")
            db_name = await conn.fetchval("SELECT current_database()")
            latency_ms = int((time.monotonic() - start) * 1000)
            return ConnectorResult(
                ok=True,
                message=f"connected to {db_name}",
                latency_ms=latency_ms,
                details={
                    "server_version": str(version).split()[1] if version else None,
                    "session_read_only": str(read_only).lower() == "on",
                },
            )
        except (asyncpg.PostgresError, OSError, ValueError) as exc:
            latency_ms = int((time.monotonic() - start) * 1000)
            return ConnectorResult(
                ok=False,
                message=_safe_error(exc),
                latency_ms=latency_ms,
            )
        finally:
            if conn is not None:
                await conn.close()

    async def discover_schemas(self) -> list[dict[str, Any]]:
        conn = await asyncpg.connect(**self._connection_kwargs())
        try:
            rows = await conn.fetch(
                """
                SELECT nspname AS schema_name
                FROM pg_namespace
                WHERE nspname NOT IN ('pg_catalog', 'information_schema')
                  AND nspname !~ '^pg_toast'
                ORDER BY nspname
                """
            )
            return [dict(r) for r in rows]
        finally:
            await conn.close()


def _safe_error(exc: Exception) -> str:
    """Render a connection error without leaking credential material."""
    text = str(exc)
    if "password" in text.lower():
        text = "authentication failed"
    return text[:500]
