"""Integration tests: dataset profiling against live local infrastructure.

Seeds a demo table with known data quality characteristics (nulls,
duplicates, numeric ranges), ingests it via discovery, profiles it, and
verifies the stored metrics carry the expected values and provenance.
"""

import asyncio
import json
import os
import uuid

import asyncpg
import pytest
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

from app.workers.worker import IngestionWorker

ADMIN_URL = os.environ.get(
    "TEST_DATABASE_URL",
    f"postgresql://dataatlas:{os.environ.get('PGPASSWORD', 'dataatlas')}"
    "@localhost:5432/dataatlas?sslmode=disable",
)
APP_URL = "postgresql://dataatlas_app:app-local-dev-only@localhost:5432/dataatlas?sslmode=disable"
APP_ROLE_PASSWORD = os.environ.get("POSTGRES_APP_PASSWORD", "app-local-dev-only")


@pytest.fixture
def profiling_setup():
    """Demo schema with a customers table; org + source + catalog dataset."""
    if not os.environ.get("APP_ENCRYPTION_KEY"):
        pytest.skip("APP_ENCRYPTION_KEY not set; integration env missing")

    schema = f"profile_demo_{uuid.uuid4().hex[:8]}"
    org_id = str(uuid.uuid4())
    source_id = str(uuid.uuid4())

    async def setup() -> None:
        admin = await asyncpg.connect(ADMIN_URL)
        try:
            await admin.execute(f"CREATE SCHEMA {schema}")  # noqa: S608 - fixture
            # The application role must be able to scan the demo schema.
            await admin.execute(  # noqa: S608 - fixture
                f"GRANT USAGE ON SCHEMA {schema} TO dataatlas_app"
            )
            await admin.execute(  # noqa: S608 - fixture DDL
                f"""
                CREATE TABLE {schema}.customers (
                    id SERIAL PRIMARY KEY,
                    email TEXT,
                    age INTEGER
                )
                """
            )
            # Known profile: 10 rows, 3 null emails, 2 duplicate ages.
            await admin.execute(  # noqa: S608 - fixture
                f"""
                INSERT INTO {schema}.customers (email, age) VALUES
                ('a@example.test', 30), ('b@example.test', 30),
                ('c@example.test', 41), (NULL, 25), (NULL, 33),
                ('d@example.test', NULL), ('e@example.test', 47),
                ('f@example.test', 19), (NULL, 52), ('g@example.test', 61)
                """
            )
            await admin.execute(  # noqa: S608 - fixture
                f"GRANT SELECT ON ALL TABLES IN SCHEMA {schema} TO dataatlas_app"
            )
        finally:
            await admin.close()

    asyncio.run(setup())

    key = bytes.fromhex(os.environ["APP_ENCRYPTION_KEY"])
    sealed = AESGCM(key).encrypt(
        b"\x05" * 12, json.dumps({"password": APP_ROLE_PASSWORD}).encode(), None
    )

    async def seed() -> None:
        conn = await asyncpg.connect(APP_URL)
        try:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)", org_id
                )
                await conn.execute(
                    "INSERT INTO organizations (id, name, slug) VALUES ($1, 'Profile Org', $2)",
                    uuid.UUID(org_id), f"profile-org-{org_id[:8]}",
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
                    VALUES ($1, $2, 'postgres', 'Profile Source', $3, 'ACTIVE', $4)
                    """,
                    uuid.UUID(source_id), uuid.UUID(org_id), f"profile-src-{source_id[:8]}",
                    json.dumps({"host": "localhost", "port": 5432, "database": "dataatlas",
                                "user": "dataatlas_app", "schemas": [schema]}),
                )
                await conn.execute(
                    """
                    INSERT INTO source_credentials
                        (data_source_id, organization_id, ciphertext, nonce)
                    VALUES ($1, $2, $3, $4)
                    """,
                    uuid.UUID(source_id), uuid.UUID(org_id), sealed, b"\x05" * 12,
                )
                await conn.execute(
                    """
                    INSERT INTO ingestion_pipelines
                        (organization_id, data_source_id, name, pipeline_type, status)
                    VALUES ($1, $2, 'Profile Source discovery', 'METADATA_DISCOVERY', 'ACTIVE')
                    """,
                    uuid.UUID(org_id), uuid.UUID(source_id),
                )
        finally:
            await conn.close()

    asyncio.run(seed())

    # Discovery populates the catalog for the new source: worker, pool and
    # usage all live inside ONE event loop.
    async def discover() -> None:
        worker = IngestionWorker()
        worker.settings.database_url = APP_URL
        worker._pool = await asyncpg.create_pool(
            worker.settings.database_url, min_size=1, max_size=2
        )
        try:
            await worker.run_discovery(source_id, org_id)
        finally:
            await worker.stop()

    asyncio.run(discover())

    yield {"org_id": org_id, "source_id": source_id, "schema": schema}

    async def teardown() -> None:
        admin = await asyncpg.connect(ADMIN_URL)
        try:
            await admin.execute(  # noqa: S608 - fixture cleanup
                f"DROP SCHEMA IF EXISTS {schema} CASCADE"
            )
        finally:
            await admin.close()

    asyncio.run(teardown())


@pytest.mark.integration
async def test_profile_stores_metrics_with_provenance(profiling_setup: dict) -> None:
    org_id = profiling_setup["org_id"]

    # Locate the discovered customers dataset.
    conn = await asyncpg.connect(APP_URL)
    async with conn.transaction():
        await conn.execute(
            "SELECT set_config('app.current_organization_id', $1, true)", org_id
        )
        dataset = await conn.fetchrow(
            """
            SELECT d.id::text AS dataset_id, d.row_count FROM datasets d
            JOIN data_assets a ON a.id = d.asset_id
            WHERE a.name = 'customers' AND d.deleted_at IS NULL
            """
        )
    await conn.close()
    assert dataset is not None
    dataset_id = dataset["dataset_id"]

    run_id = str(uuid.uuid4())

    async def record_run() -> None:
        conn = await asyncpg.connect(APP_URL)
        try:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)", org_id
                )
                pipeline = await conn.fetchval(
                    """
                    SELECT p.id FROM ingestion_pipelines p
                    WHERE p.data_source_id = $1 AND p.deleted_at IS NULL LIMIT 1
                    """,
                    uuid.UUID(profiling_setup["source_id"]),
                )
                await conn.execute(
                    """
                    INSERT INTO ingestion_runs (id, pipeline_id, organization_id, trigger, status)
                    VALUES ($1, $2, $3, 'MANUAL', 'RUNNING')
                    """,
                    uuid.UUID(run_id), pipeline, uuid.UUID(org_id),
                )
        finally:
            await conn.close()

    await record_run()

    worker = IngestionWorker()
    worker.settings.database_url = APP_URL
    worker._pool = await asyncpg.create_pool(  # noqa: SLF001 - test setup
        worker.settings.database_url, min_size=1, max_size=2
    )
    try:
        await worker.profile_dataset(run_id, dataset_id, org_id)
    finally:
        await worker.stop()

    # Verify stored metrics.
    conn = await asyncpg.connect(APP_URL)
    try:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_organization_id', $1, true)", org_id
            )
            snapshot = await conn.fetchrow(
                """
                SELECT row_count, column_count,
                       metric_method->>'row_count' AS row_count_method
                FROM profile_snapshots WHERE dataset_id = $1
                """,
                uuid.UUID(dataset_id),
            )
            metrics = await conn.fetch(
                """
                SELECT c.name, m.metric_name, m.metric_value, m.value_type
                FROM column_profiles m
                JOIN dataset_columns c ON c.id = m.dataset_column_id
                JOIN profile_snapshots ps ON ps.profiling_run_id = m.profiling_run_id
                WHERE ps.dataset_id = $1
                """,
                uuid.UUID(dataset_id),
            )
    finally:
        await conn.close()

    assert snapshot is not None
    assert snapshot["row_count"] == 10
    # Planner estimate when analyzed; bounded-sample count otherwise.
    assert snapshot["row_count_method"] in ("estimate", "sample")
    assert snapshot["column_count"] == 3

    by_metric = {(m["name"], m["metric_name"]): m for m in metrics}
    email_nulls = by_metric[("email", "null_count")]["metric_value"]
    assert email_nulls == 3.0
    email_uniqueness = by_metric[("email", "uniqueness_percentage")]["metric_value"]
    # 7 distinct non-null emails over 10 sampled rows.
    assert email_uniqueness == 70.0
    age_min = by_metric[("age", "min_value")]["metric_value"]
    assert age_min == 19.0
    age_max = by_metric[("age", "max_value")]["metric_value"]
    assert age_max == 61.0

    # Provenance: every stored metric records how it was computed.
    assert all(m["value_type"] in ("EXACT", "APPROXIMATE", "SAMPLE", "INFERRED") for m in metrics)
    assert all(m["value_type"] == "SAMPLE" for m in metrics)
