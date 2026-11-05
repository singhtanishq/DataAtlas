"""CSV and Parquet file connectors built on DuckDB.

Files are scanned with DuckDB's bounded readers — never fully loaded into
memory. Profiling and quality checks run against the DuckDB connection with
read-only semantics.
"""

from __future__ import annotations

import hashlib
import os
import time
from pathlib import Path
from typing import Any, ClassVar

import duckdb

from app.connectors.base import ConnectorError, ConnectorResult, DataConnector, connector_settings


def _validate_path(path: str) -> Path:
    """File sources resolve to an existing, readable local file."""
    candidate = Path(path).expanduser().resolve()
    if not candidate.is_file():
        raise ConnectorError(f"file not found: {path}")
    if candidate.suffix.lower() not in (".csv", ".parquet"):
        raise ConnectorError(f"unsupported file type: {candidate.suffix}")
    return candidate


def _read_expr(path: Path, sample_only: bool = False) -> str:
    """DuckDB read expression for one file (bounded when sampling)."""
    if path.suffix.lower() == ".csv":
        read = f"read_csv_auto('{path}')"
    else:
        read = f"read_parquet('{path}')"
    if sample_only:
        read += f" LIMIT {connector_settings()['max_rows']}"
    return read


class FileConnector(DataConnector):
    """Base for file-based connectors (CSV and Parquet)."""

    REQUIRED_CONFIG = ("path",)

    def _file(self) -> Path:
        missing = [k for k in self.REQUIRED_CONFIG if not self.config.get(k)]
        if missing:
            raise ConnectorError(f"missing required configuration: {', '.join(missing)}")
        return _validate_path(str(self.config["path"]))

    async def validate_connection(self) -> ConnectorResult:
        start = time.monotonic()
        try:
            path = self._file()
            con = duckdb.connect(database=":memory:")
            try:
                count_row = con.execute(
                    f"SELECT count(*) FROM {_read_expr(path, sample_only=True)}"  # noqa: S608
                ).fetchone()
                count = count_row[0] if count_row else 0
            finally:
                con.close()
            stat = os.stat(path)
            return ConnectorResult(
                ok=True,
                message=f"file readable: {path.name}",
                latency_ms=int((time.monotonic() - start) * 1000),
                details={
                    "size_bytes": stat.st_size,
                    "modified_at": stat.st_mtime,
                    "sample_rows": int(count),
                    "fingerprint": hashlib.sha256(
                        f"{path}:{stat.st_size}:{stat.st_mtime}".encode()
                    ).hexdigest()[:16],
                },
            )
        except (ConnectorError, duckdb.Error, OSError) as exc:
            return ConnectorResult(
                ok=False,
                message=str(exc)[:300],
                latency_ms=int((time.monotonic() - start) * 1000),
            )

    async def discover_columns(self, dataset: dict[str, Any]) -> list[dict[str, Any]]:
        path = self._file()
        con = duckdb.connect(database=":memory:")
        try:
            rows = con.execute(
                f"DESCRIBE SELECT * FROM {_read_expr(path, sample_only=True)}"  # noqa: S608
            ).fetchall()
            return [
                {"name": r[0], "ordinal": i + 1, "data_type": r[1], "nullable": True}
                for i, r in enumerate(rows)
            ]
        finally:
            con.close()


class CSVConnector(FileConnector):
    """CSV file connector via DuckDB."""

    connector_key: ClassVar[str] = "csv_file"
    platform: ClassVar[str] = "file"

    async def discover_datasets(self) -> list[dict[str, Any]]:
        path = self._file()
        con = duckdb.connect(database=":memory:")
        try:
            row = con.execute(
                f"SELECT count(*) FROM {_read_expr(path)}"  # noqa: S608
            ).fetchone()
            row_count = row[0] if row else 0
        finally:
            con.close()
        return [{
            "namespace": "files",
            "name": path.name,
            "asset_type": "FILE",
            "row_estimate": int(row_count),
            "location": str(path),
            "size_bytes": os.stat(path).st_size,
        }]


class ParquetConnector(FileConnector):
    """Parquet file connector via DuckDB (metadata-only row counts)."""

    connector_key: ClassVar[str] = "parquet_file"
    platform: ClassVar[str] = "file"

    async def discover_datasets(self) -> list[dict[str, Any]]:
        path = self._file()
        con = duckdb.connect(database=":memory:")
        try:
            row = con.execute(
                f"SELECT count(*) FROM {_read_expr(path)}"  # noqa: S608
            ).fetchone()
            row_count = row[0] if row else 0
        finally:
            con.close()
        return [{
            "namespace": "files",
            "name": path.name,
            "asset_type": "FILE",
            "row_estimate": int(row_count),
            "location": str(path),
            "size_bytes": os.stat(path).st_size,
        }]
