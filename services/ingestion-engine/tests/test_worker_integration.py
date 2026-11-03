"""Integration tests: connector + worker against live local infrastructure.

Requires the Docker stack (postgres, nats) and a migrated dataatlas
database. Run via `scripts/test.sh integration` or directly with
`uv run pytest -m integration`.
"""

import asyncio
import json
import os
import uuid

import asyncpg
import nats as nats_client
import pytest

from app.connectors.postgres import PostgreSQLConnector
from app.workers.worker import VALIDATE_SUBJECT, IngestionWorker

_pg_password = os.environ.get("PGPASSWORD", "dataatlas")
ADMIN_URL = os.environ.get(
    "TEST_DATABASE_URL",
    f"postgresql://dataatlas:{_pg_password}@localhost:5432/postgres?sslmode=disable",
)
APP_URL = "postgresql://dataatlas_app:app-local-dev-only@localhost:5432/dataatlas?sslmode=disable"
NATS_URL = os.environ.get("TEST_NATS_URL", "nats://localhost:4222")
APP_ROLE_PASSWORD = os.environ.get("POSTGRES_APP_PASSWORD", "app-local-dev-only")


@pytest.fixture
def live_db_available() -> None:
    """Skip unless the local infrastructure is reachable."""
    try:
        conn = asyncio_run_connect()
    except Exception as exc:  # noqa: BLE001
        pytest.skip(f"local postgres unavailable: {exc}")
    else:
        conn.close()


def asyncio_run_connect() -> asyncpg.Connection:
    async def _connect() -> asyncpg.Connection:
        conn = await asyncpg.connect(ADMIN_URL)
        await conn.close()
        return conn

    return asyncio.run(_connect())


@pytest.fixture
def org_with_source(live_db_available: None):
    """Migrate nothing; assume the dev database is migrated. Creates an org,
    a connector definition and a source with credentials for the local
    postgres, encrypted with the local APP_ENCRYPTION_KEY."""
    for var in ("APP_ENCRYPTION_KEY", "PGPASSWORD"):
        if not os.environ.get(var):
            pytest.skip(f"{var} not set; integration env missing")

    from cryptography.hazmat.primitives.ciphers.aead import AESGCM

    key = bytes.fromhex(os.environ["APP_ENCRYPTION_KEY"])
    org_id = str(uuid.uuid4())
    source_id = str(uuid.uuid4())
    credentials = {"password": APP_ROLE_PASSWORD}
    nonce = b"\x02" * 12
    sealed = AESGCM(key).encrypt(nonce, json.dumps(credentials).encode(), None)

    async def seed() -> None:
        conn = await asyncpg.connect(APP_URL)
        try:
            # The tenant GUC is transaction-local; all scoped writes run in
            # one transaction so RLS accepts them.
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    org_id,
                )
                await conn.execute(
                    "INSERT INTO organizations (id, name, slug) VALUES ($1, 'Worker Org', $2)",
                    uuid.UUID(org_id),
                    f"worker-org-{org_id[:8]}",
                )
                await conn.execute(
                    """
                    INSERT INTO connector_definitions (key, display_name, platform)
                    VALUES ('postgres', 'PostgreSQL', 'postgresql')
                    ON CONFLICT (key) DO NOTHING
                    """
                )
                await conn.execute(
                    """
                    INSERT INTO data_sources
                        (id, organization_id, connector_key, name, slug, status, config)
                    VALUES ($1, $2, 'postgres', 'Worker Test Source', $3, 'CONFIGURED', $4)
                    """,
                    uuid.UUID(source_id),
                    uuid.UUID(org_id),
                    f"worker-test-{source_id[:8]}",
                    json.dumps({
                        "host": "localhost", "port": 5432,
                        "database": "dataatlas", "user": "dataatlas_app",
                    }),
                )
                await conn.execute(
                    """
                    INSERT INTO source_credentials
                        (data_source_id, organization_id, ciphertext, nonce)
                    VALUES ($1, $2, $3, $4)
                    """,
                    uuid.UUID(source_id),
                    uuid.UUID(org_id),
                    sealed,
                    nonce,
                )
        finally:
            await conn.close()

    asyncio.run(seed())

    yield {"org_id": org_id, "source_id": source_id}


@pytest.mark.integration
async def test_postgres_connector_validates_live(live_db_available: None) -> None:
    connector = PostgreSQLConnector(
        config={
            "host": "localhost", "port": 5432,
            "database": "dataatlas", "user": "dataatlas_app",
        },
        credentials={"password": APP_ROLE_PASSWORD},
    )
    result = await connector.validate_connection()
    assert result.ok, result.message
    assert result.details.get("session_read_only") is True
    assert result.latency_ms >= 0


@pytest.mark.integration
async def test_postgres_connector_rejects_bad_credentials(live_db_available: None) -> None:
    connector = PostgreSQLConnector(
        config={
            "host": "localhost", "port": 5432,
            "database": "dataatlas", "user": "dataatlas_app",
        },
        credentials={"password": "definitely-wrong"},
    )
    result = await connector.validate_connection()
    assert not result.ok
    assert "authentication failed" in result.message


@pytest.mark.integration
async def test_worker_validates_source_end_to_end(org_with_source: dict) -> None:
    """Full path: worker loads encrypted credentials from the database,
    decrypts them, connects, and reports success."""
    worker = IngestionWorker()
    patch_settings(worker)
    await worker.start()
    try:
        result = await worker.validate_source_connection(
            org_with_source["source_id"], org_with_source["org_id"]
        )
    finally:
        await worker.stop()
    assert result.ok, result.message


@pytest.mark.integration
async def test_worker_reports_unknown_source(org_with_source: dict) -> None:
    worker = IngestionWorker()
    patch_settings(worker)
    await worker.start()
    try:
        result = await worker.validate_source_connection(
            str(uuid.uuid4()), org_with_source["org_id"]
        )
    finally:
        await worker.stop()
    assert not result.ok
    assert "not found" in result.message


@pytest.mark.integration
async def test_worker_replies_over_nats(org_with_source: dict) -> None:
    """Request/reply through the real broker with a live worker."""
    worker = IngestionWorker()
    patch_settings(worker)
    await worker.start()
    try:
        nc = await nats_client.connect(NATS_URL)
        try:
            request = json.dumps({
                "source_id": org_with_source["source_id"],
                "organization_id": org_with_source["org_id"],
            }).encode()
            reply = await nc.request(VALIDATE_SUBJECT, request, timeout=20)
            payload = json.loads(reply.data)
            assert payload["ok"] is True, payload["message"]
        finally:
            await nc.drain()
    finally:
        await worker.stop()


def patch_settings(worker: IngestionWorker):
    """Bind runtime env values into the worker settings."""
    worker.settings.database_url = APP_URL
    worker.settings.nats_url = NATS_URL
    worker.settings.app_encryption_key = os.environ["APP_ENCRYPTION_KEY"]
    return worker.settings
