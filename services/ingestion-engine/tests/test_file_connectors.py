"""File connector tests: CSV and Parquet discovery, profiling of file data."""

import csv
import uuid
from pathlib import Path

import duckdb
import pytest

from app.connectors.file_connector import CSVConnector, ParquetConnector
from app.connectors.registry import get_connector_class


@pytest.fixture
def sample_csv(tmp_path: Path) -> Path:
    path = tmp_path / f"orders_{uuid.uuid4().hex[:8]}.csv"
    with path.open("w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(["order_id", "customer", "amount", "status"])
        writer.writerow(["1", "Acme", "100.50", "paid"])
        writer.writerow(["2", "Borealis", "75.00", "pending"])
        writer.writerow(["3", "Acme", "", "paid"])
    return path


@pytest.fixture
def sample_parquet(tmp_path: Path, sample_csv: Path) -> Path:
    path = tmp_path / f"orders_{uuid.uuid4().hex[:8]}.parquet"
    con = duckdb.connect(database=":memory:")
    try:
        con.execute(
            f"COPY (SELECT * FROM read_csv_auto('{sample_csv}')) "  # noqa: S608
            f"TO '{path}' (FORMAT PARQUET)"
        )
    finally:
        con.close()
    return path


async def test_registry_exposes_file_connectors() -> None:
    assert get_connector_class("csv_file") is CSVConnector
    assert get_connector_class("parquet_file") is ParquetConnector


async def test_csv_validate_and_discover(sample_csv: Path) -> None:
    connector = CSVConnector(config={"path": str(sample_csv)}, credentials={})
    result = await connector.validate_connection()
    assert result.ok, result.message
    assert result.details["sample_rows"] == 3
    assert result.details["size_bytes"] > 0
    assert len(result.details["fingerprint"]) == 16

    datasets = await connector.discover_datasets()
    assert len(datasets) == 1
    assert datasets[0]["name"] == sample_csv.name
    assert datasets[0]["row_estimate"] == 3
    assert datasets[0]["location"] == str(sample_csv)

    columns = await connector.discover_columns({})
    names = [c["name"] for c in columns]
    assert names == ["order_id", "customer", "amount", "status"]


async def test_parquet_validate_and_discover(sample_parquet: Path) -> None:
    connector = ParquetConnector(config={"path": str(sample_parquet)}, credentials={})
    result = await connector.validate_connection()
    assert result.ok, result.message

    columns = await connector.discover_columns({})
    assert [c["name"] for c in columns] == ["order_id", "customer", "amount", "status"]


async def test_missing_file_rejected(tmp_path: Path) -> None:
    connector = CSVConnector(config={"path": str(tmp_path / "missing.csv")}, credentials={})
    result = await connector.validate_connection()
    assert not result.ok
    assert "file not found" in result.message


async def test_unsupported_type_rejected(tmp_path: Path) -> None:
    path = tmp_path / "data.txt"
    path.write_text("hello")
    connector = CSVConnector(config={"path": str(path)}, credentials={})
    result = await connector.validate_connection()
    assert not result.ok
    assert "unsupported file type" in result.message


async def test_missing_config_rejected() -> None:
    connector = CSVConnector(config={}, credentials={})
    result = await connector.validate_connection()
    assert not result.ok
    assert "missing required configuration" in result.message
