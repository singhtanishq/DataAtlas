"""Connector registry: maps connector keys to implementations."""

from __future__ import annotations

from app.connectors.base import DataConnector
from app.connectors.file_connector import CSVConnector, ParquetConnector
from app.connectors.postgres import PostgreSQLConnector

_REGISTRY: dict[str, type[DataConnector]] = {
    PostgreSQLConnector.connector_key: PostgreSQLConnector,
    CSVConnector.connector_key: CSVConnector,
    ParquetConnector.connector_key: ParquetConnector,
}


def register(connector_cls: type[DataConnector]) -> type[DataConnector]:
    """Register a connector class under its declared key."""
    _REGISTRY[connector_cls.connector_key] = connector_cls
    return connector_cls


def get_connector_class(key: str) -> type[DataConnector]:
    try:
        return _REGISTRY[key]
    except KeyError as exc:
        raise LookupError(f"unknown connector {key!r}") from exc


def known_connectors() -> list[str]:
    return sorted(_REGISTRY)
