"""Ingestion worker: consumes connector jobs from NATS.

The worker is the only component that decrypts source credentials. Secrets
never travel through the broker: workers receive a source id, load the
encrypted credentials from PostgreSQL, and decrypt locally.
"""

from __future__ import annotations

import asyncio
import json
import logging
import time
from typing import Any

import asyncpg
import nats
from nats.aio.msg import Msg

from app.connectors.base import ConnectorResult
from app.connectors.registry import get_connector_class
from app.core.config import get_settings
from app.core.crypto import CredentialCipher

logger = logging.getLogger("dataatlas.worker")

VALIDATE_SUBJECT = "dataatlas.ingestion.validate_connection"
WORKER_QUEUE = "ingestion-workers"


class IngestionWorker:
    """NATS request/reply worker for connector operations."""

    def __init__(self) -> None:
        self.settings = get_settings()
        self.cipher = CredentialCipher(self.settings.app_encryption_key or "")
        self._nc: Any = None
        self._pool: asyncpg.Pool | None = None

    async def start(self) -> None:
        self._pool = await asyncpg.create_pool(
            self.settings.database_url, min_size=1, max_size=4,
            command_timeout=self.settings.connector_query_timeout_seconds,
        )
        self._nc = await nats.connect(
            self.settings.nats_url,
            connect_timeout=int(self.settings.connector_connect_timeout_seconds),
            max_reconnect_attempts=-1,
        )
        await self._nc.subscribe(
            VALIDATE_SUBJECT, queue=WORKER_QUEUE, cb=self._handle_validate
        )
        logger.info("worker subscribed", extra={"subject": VALIDATE_SUBJECT})

    async def stop(self) -> None:
        if self._nc is not None:
            await self._nc.drain()
        if self._pool is not None:
            await self._pool.close()

    # -- request handling --------------------------------------------------

    async def _handle_validate(self, msg: Msg) -> None:
        try:
            request = json.loads(msg.data)
        except json.JSONDecodeError:
            await msg.respond(b'{"ok": false, "message": "invalid request payload"}')
            return

        source_id = request.get("source_id")
        organization_id = request.get("organization_id")
        if not source_id or not organization_id:
            await msg.respond(b'{"ok": false, "message": "source_id and organization_id required"}')
            return

        result = await self.validate_source_connection(source_id, organization_id)
        reply = json.dumps({
            "ok": result.ok,
            "message": result.message,
            "latency_ms": result.latency_ms,
            "details": result.details,
        })
        await msg.respond(reply.encode())

    async def validate_source_connection(
        self, source_id: str, organization_id: str
    ) -> ConnectorResult:
        """Load a source's credentials and validate its connection."""
        assert self._pool is not None
        started = time.monotonic()
        async with self._pool.acquire() as conn:
            # The tenant context is transaction-local by design; run the
            # read inside one transaction so RLS sees it and it cannot leak
            # into later reuses of this pooled connection.
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                row = await conn.fetchrow(
                    """
                    SELECT ds.connector_key, ds.config,
                           sc.ciphertext, sc.nonce
                    FROM data_sources ds
                    JOIN source_credentials sc ON sc.data_source_id = ds.id
                    WHERE ds.id = $1 AND ds.deleted_at IS NULL
                    """,
                    source_id,
                )
        if row is None:
            return ConnectorResult(ok=False, message="source not found")

        try:
            credentials = self.cipher.decrypt_json(bytes(row["ciphertext"]), bytes(row["nonce"]))
        except Exception as exc:
            logger.warning("credential decryption failed", extra={"error": str(exc)})
            return ConnectorResult(ok=False, message="credential decryption failed")

        try:
            connector_cls = get_connector_class(row["connector_key"])
        except LookupError as exc:
            return ConnectorResult(ok=False, message=str(exc))

        raw_config = row["config"]
        if isinstance(raw_config, dict):
            config = raw_config
        else:
            config = json.loads(raw_config or "{}")
        connector = connector_cls(config=config, credentials=credentials)
        try:
            result = await asyncio.wait_for(
                connector.validate_connection(),
                timeout=self.settings.connector_connect_timeout_seconds * 2,
            )
        except TimeoutError:
            elapsed = int((time.monotonic() - started) * 1000)
            return ConnectorResult(
                ok=False, message="connection test timed out", latency_ms=elapsed
            )
        return result


async def main() -> None:  # pragma: no cover - process entrypoint
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")
    worker = IngestionWorker()
    await worker.start()
    stop = asyncio.Event()
    try:
        await stop.wait()
    except (KeyboardInterrupt, asyncio.CancelledError):
        pass
    finally:
        await worker.stop()


if __name__ == "__main__":  # pragma: no cover
    asyncio.run(main())
