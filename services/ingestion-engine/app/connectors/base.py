"""Connector framework for DataAtlas ingestion.

Connectors extract metadata from external data systems through a uniform
interface. Every connector is read-only by contract: the framework forbids
write statements and enforces timeouts and row limits.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Any, ClassVar

from app.core.config import get_settings


@dataclass(frozen=True)
class ConnectorResult:
    """Outcome of a connector operation with evidence."""

    ok: bool
    message: str
    latency_ms: int = 0
    details: dict[str, Any] = field(default_factory=dict)


class ConnectorError(Exception):
    """Raised when a connector operation fails."""


class DataConnector(ABC):
    """Uniform contract implemented by every DataAtlas connector.

    Subclasses declare a unique ``connector_key`` and a ``platform`` family.
    The construction contract receives the non-secret ``config`` mapping and
    the decrypted ``credentials`` mapping; implementations must never log
    credential values.
    """

    connector_key: ClassVar[str]
    platform: ClassVar[str]

    def __init__(self, config: dict[str, Any], credentials: dict[str, Any]) -> None:
        self.config = config
        self.credentials = credentials

    # -- lifecycle ---------------------------------------------------------

    @abstractmethod
    async def validate_connection(self) -> ConnectorResult:
        """Verify connectivity and permissions. Must be side-effect free."""

    # -- discovery (implemented in later milestones per connector) ----------

    async def discover_databases(self) -> list[dict[str, Any]]:
        raise NotImplementedError(f"{self.connector_key} does not implement discover_databases")

    async def discover_schemas(self) -> list[dict[str, Any]]:
        raise NotImplementedError(f"{self.connector_key} does not implement discover_schemas")

    async def discover_datasets(self) -> list[dict[str, Any]]:
        raise NotImplementedError(f"{self.connector_key} does not implement discover_datasets")

    async def discover_columns(self, dataset: dict[str, Any]) -> list[dict[str, Any]]:
        raise NotImplementedError(f"{self.connector_key} does not implement discover_columns")

    async def get_checkpoint(self) -> dict[str, Any]:
        return {}


def connector_settings() -> dict[str, Any]:
    """Common safety limits shared by all connectors."""
    settings = get_settings()
    return {
        "connect_timeout": settings.connector_connect_timeout_seconds,
        "query_timeout": settings.connector_query_timeout_seconds,
        "max_rows": settings.connector_max_rows,
    }
