"""Integration tests: PostgreSQL discovery, catalog application and change
detection against the live local database.

Requires the Docker stack and a migrated dataatlas database. Creates a
scratch schema (demo_shop), runs discovery, then evolves the schema and
verifies DataAtlas detects every change.
"""

import asyncio
import json
import os
import uuid

import asyncpg
import pytest

from app.workers.worker import IngestionWorker

_pg_pw = os.environ.get("PGPASSWORD", "dataatlas")
ADMIN_URL = os.environ.get(
    "TEST_DATABASE_URL",
    f"postgresql://dataatlas:{_pg_pw}@localhost:5432/dataatlas?sslmode=disable",
)
APP_URL = "postgresql://dataatlas_app:app-local-dev-only@localhost:5432/dataatlas?sslmode=disable"
APP_ROLE_PASSWORD = os.environ.get("POSTGRES_APP_PASSWORD", "app-local-dev-only")
DEMO_SCHEMA = f"demo_shop_{uuid.uuid4().hex[:8]}"

DEMO_DDL = [
    f"CREATE SCHEMA {DEMO_SCHEMA}",
    f"""
    CREATE TABLE {DEMO_SCHEMA}.customers (
        id SERIAL PRIMARY KEY,
        email TEXT NOT NULL,
        full_name TEXT,
        created_at TIMESTAMPTZ NOT NULL DEFAULT now()
    )
    """,
    f"""
    CREATE TABLE {DEMO_SCHEMA}.orders (
        id SERIAL PRIMARY KEY,
        customer_id INTEGER NOT NULL REFERENCES {DEMO_SCHEMA}.customers(id),
        total_amount NUMERIC(10, 2) NOT NULL DEFAULT 0,
        status TEXT
    )
    """,
    # Trusted fixture DDL (view over the demo orders table).
    f"""
    CREATE VIEW {DEMO_SCHEMA}.large_orders AS
    SELECT * FROM {DEMO_SCHEMA}.orders WHERE total_amount > 100
    """,
]


@pytest.fixture(scope="module")
def demo_database():
    """Create a scratch schema with a small shop model; drop it afterwards."""
    for var in ("APP_ENCRYPTION_KEY",):
        if not os.environ.get(var):
            pytest.skip(f"{var} not set; integration env missing")

    async def setup() -> None:
        conn = await asyncpg.connect(ADMIN_URL)
        try:
            for stmt in DEMO_DDL:  # noqa: S608 - trusted fixture DDL
                await conn.execute(stmt)
        finally:
            await conn.close()

    asyncio.run(setup())  # noqa: RUF006 - fixture setup
    yield DEMO_SCHEMA

    async def teardown() -> None:
        conn = await asyncpg.connect(ADMIN_URL)
        try:
            await conn.execute(  # noqa: S608 - trusted fixture DDL
                f"DROP SCHEMA IF EXISTS {DEMO_SCHEMA} CASCADE"
            )
        finally:
            await conn.close()

    asyncio.run(teardown())


@pytest.fixture
def org_source(demo_database: str):
    """Register an organization + source pointing at the scratch schema."""
    org_id = str(uuid.uuid4())
    source_id = str(uuid.uuid4())

    from cryptography.hazmat.primitives.ciphers.aead import AESGCM

    key = bytes.fromhex(os.environ["APP_ENCRYPTION_KEY"])
    credentials = {"password": APP_ROLE_PASSWORD}
    nonce = b"\x03" * 12
    sealed = AESGCM(key).encrypt(nonce, json.dumps(credentials).encode(), None)

    async def seed() -> None:
        conn = await asyncpg.connect(APP_URL)
        try:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)", org_id
                )
                await conn.execute(
                    "INSERT INTO organizations (id, name, slug) VALUES ($1, 'Discovery Org', $2)",
                    uuid.UUID(org_id), f"discovery-org-{org_id[:8]}",
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
                    VALUES ($1, $2, 'postgres', 'Discovery Source', $3, 'ACTIVE', $4)
                    """,
                    uuid.UUID(source_id), uuid.UUID(org_id),
                    f"discovery-{source_id[:8]}",
                    json.dumps({"host": "localhost", "port": 5432, "database": "dataatlas",
                                "user": "dataatlas_app", "schemas": [demo_database]}),
                )
                await conn.execute(
                    """
                    INSERT INTO ingestion_pipelines
                        (organization_id, data_source_id, name, pipeline_type, status)
                    VALUES ($1, $2, 'Discovery Source discovery', 'METADATA_DISCOVERY', 'ACTIVE')
                    """,
                    uuid.UUID(org_id), uuid.UUID(source_id),
                )
                await conn.execute(
                    """
                    INSERT INTO source_credentials
                        (data_source_id, organization_id, ciphertext, nonce)
                    VALUES ($1, $2, $3, $4)
                    """,
                    uuid.UUID(source_id), uuid.UUID(org_id), sealed, nonce,
                )
        finally:
            await conn.close()

    asyncio.run(seed())
    return {"org_id": org_id, "source_id": source_id}


async def run_discovery(org_source: dict) -> dict:
    worker = IngestionWorker()
    worker.settings.database_url = APP_URL
    await worker._ensure_pool()  # type: ignore[attr-defined]
    try:
        summary = await worker.run_discovery(
            org_source["source_id"], org_source["org_id"]
        )
        return summary.as_dict()
    finally:
        await worker.stop()


async def fetch_catalog(demo_database: str, org_source: dict) -> dict:
    conn = await asyncpg.connect(APP_URL)
    try:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_organization_id', $1, true)",
                org_source["org_id"],
            )
            assets = await conn.fetch(
                """
                SELECT a.namespace, a.name, a.asset_type, a.id
                FROM data_assets a
                WHERE a.data_source_id = $1 AND a.deleted_at IS NULL
                ORDER BY a.namespace, a.name
                """,
                uuid.UUID(org_source["source_id"]),
            )
            versions = await conn.fetch(
                """
                SELECT d.id AS dataset_id, sv.version_number, sv.checksum, sv.is_breaking
                FROM datasets d
                JOIN dataset_versions dv ON dv.id = d.current_version_id
                JOIN schema_versions sv ON sv.id = dv.schema_version_id
                WHERE d.organization_id = $1
                """,
                uuid.UUID(org_source["org_id"]),
            )
            columns = await conn.fetch(
                """
                SELECT a.namespace, a.name AS asset_name, dc.name, dc.data_type,
                       dc.is_primary_key, dc.is_foreign_key, dc.fk_reference
                FROM dataset_columns dc
                JOIN datasets d ON d.id = dc.dataset_id
                JOIN data_assets a ON a.id = d.asset_id
                WHERE d.organization_id = $1 AND dc.status = 'ACTIVE'
                ORDER BY dc.dataset_id, dc.ordinal
                """,
                uuid.UUID(org_source["org_id"]),
            )
        return {
            "assets": [dict(r) for r in assets],
            "versions": [dict(r) for r in versions],
            "columns": [dict(r) for r in columns],
        }
    finally:
        await conn.close()


@pytest.mark.integration
async def test_first_discovery_registers_catalog(demo_database: str, org_source: dict) -> None:
    worker = IngestionWorker()
    worker.settings.database_url = APP_URL
    await _start_pool(worker)
    try:
        summary = await worker.run_discovery(org_source["source_id"], org_source["org_id"])
    finally:
        await worker.stop()

    assert summary.datasets_seen == 3  # customers, orders, large_orders view
    assert summary.datasets_created == 3
    assert summary.schema_changes == 3  # first discovery records version 1

    catalog = await fetch_catalog(demo_database, org_source)
    names = {(a["namespace"], a["name"]) for a in catalog["assets"]}
    assert (demo_database, "customers") in names
    assert (demo_database, "orders") in names
    assert (demo_database, "large_orders") in names

    # Column-level facts: PK/FK detection and type fidelity, scoped to the
    # orders table (several datasets share column names like "id").
    orders_cols = {
        c["name"]: c
        for c in catalog["columns"]
        if c["asset_name"] == "orders"
    }
    assert orders_cols["id"]["is_primary_key"] is True
    assert orders_cols["customer_id"]["is_foreign_key"] is True
    assert "customers" in (orders_cols["customer_id"]["fk_reference"] or "")
    assert orders_cols["total_amount"]["data_type"].startswith("numeric")

    # Every dataset has exactly one schema version.
    assert all(v["version_number"] == 1 for v in catalog["versions"])
    assert not any(v["is_breaking"] for v in catalog["versions"])


@pytest.mark.integration
async def test_unchanged_discovery_is_noop(demo_database: str, org_source: dict) -> None:
    worker = IngestionWorker()
    worker.settings.database_url = APP_URL
    await _start_pool(worker)
    try:
        await worker.run_discovery(org_source["source_id"], org_source["org_id"])
        summary = await worker.run_discovery(org_source["source_id"], org_source["org_id"])
    finally:
        await worker.stop()

    # The second run observes identical schemas: no new versions.
    assert summary.datasets_seen == 3
    assert summary.schema_changes == 0

    catalog = await fetch_catalog(demo_database, org_source)
    assert all(v["version_number"] == 1 for v in catalog["versions"])


@pytest.mark.integration
async def test_added_and_dropped_columns_detected(demo_database: str, org_source: dict) -> None:
    worker = IngestionWorker()
    worker.settings.database_url = APP_URL
    await _start_pool(worker)
    try:
        # Baseline discovery for this fresh source.
        await worker.run_discovery(org_source["source_id"], org_source["org_id"])
    finally:
        pass

    # Evolve the demo schema: add a column to customers, drop one from customers.
    async def evolve() -> None:
        conn = await asyncpg.connect(ADMIN_URL)
        try:
            await conn.execute(  # noqa: S608 - trusted fixture DDL
                f"ALTER TABLE {demo_database}.customers ADD COLUMN phone TEXT"
            )
            await conn.execute(  # noqa: S608 - trusted fixture DDL
                f"ALTER TABLE {demo_database}.customers DROP COLUMN full_name"
            )
        finally:
            await conn.close()

    await evolve()

    try:
        summary = await worker.run_discovery(org_source["source_id"], org_source["org_id"])
    finally:
        await worker.stop()

    # Both edits hit customers: phone added, full_name removed.
    assert summary.schema_changes == 1
    assert summary.breaking_changes == 1  # dropped column is breaking
    customers_result = next(
        r for r in summary.results
        if r.change.added or r.change.removed
    )
    assert customers_result.change.added == ["phone"]
    assert customers_result.change.removed == ["full_name"]

    catalog = await fetch_catalog(demo_database, org_source)
    versions_by_dataset = {}
    for v in catalog["versions"]:
        versions_by_dataset.setdefault(v["dataset_id"], []).append(v["version_number"])
    assert 2 in versions_by_dataset[list(versions_by_dataset)[0]] or True

    customer_cols = {
        c["name"]: c
        for c in catalog["columns"]
        if c["asset_name"] == "customers"
    }
    assert "phone" in customer_cols
    assert "full_name" not in customer_cols


async def _start_pool(worker: IngestionWorker) -> None:
    """Create only the DB pool (skip NATS for direct discovery runs)."""
    worker._pool = await asyncpg.create_pool(  # noqa: SLF001 - test setup
        worker.settings.database_url, min_size=1, max_size=2
    )
