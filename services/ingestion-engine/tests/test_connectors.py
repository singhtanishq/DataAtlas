"""Connector framework tests."""

import pytest

from app.connectors.base import (
    ConnectorError,
    ConnectorResult,
    DataConnector,
    connector_settings,
)
from app.connectors.postgres import PostgreSQLConnector
from app.connectors.registry import get_connector_class, known_connectors, register


class DummyConnector(DataConnector):
    connector_key = "dummy"
    platform = "dummy"

    async def validate_connection(self) -> ConnectorResult:
        return ConnectorResult(ok=True, message="ok")


def test_registry_lookup() -> None:
    assert get_connector_class("postgres") is PostgreSQLConnector
    register(DummyConnector)
    assert get_connector_class("dummy") is DummyConnector
    assert "postgres" in known_connectors()


def test_registry_unknown_key() -> None:
    with pytest.raises(LookupError):
        get_connector_class("nonexistent")


def test_missing_required_config_rejected() -> None:
    connector = PostgreSQLConnector(config={}, credentials={"password": "x"})
    with pytest.raises(ConnectorError):
        connector._connection_kwargs()


def test_missing_password_rejected() -> None:
    connector = PostgreSQLConnector(
        config={"host": "localhost", "database": "db"}, credentials={}
    )
    with pytest.raises(ConnectorError):
        connector._connection_kwargs()


async def test_base_discovery_unimplemented() -> None:
    connector = DummyConnector(config={}, credentials={})
    with pytest.raises(NotImplementedError):
        await connector.discover_schemas()


def test_connector_settings_include_safety_limits() -> None:
    limits = connector_settings()
    assert limits["connect_timeout"] > 0
    assert limits["query_timeout"] > 0
    assert limits["max_rows"] > 0


def test_connection_kwargs_enforce_read_only_session() -> None:
    connector = PostgreSQLConnector(
        config={"host": "localhost", "database": "db", "port": 5432},
        credentials={"password": "secret"},
    )
    kwargs = connector._connection_kwargs()
    server_settings = kwargs["server_settings"]
    assert server_settings["default_transaction_read_only"] == "on"
    assert int(server_settings["statement_timeout"]) > 0
