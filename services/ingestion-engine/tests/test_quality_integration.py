"""Integration tests: quality check execution against live infrastructure.

Seeds a table with known defects and verifies each structured check plus
the CUSTOM_SQL safety model (rejection of writes, multi-statements and
non-SELECT roots).
"""

import asyncio
import json
import os
import uuid

import asyncpg
import pytest
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

from app.quality.engine import QualityEngine
from app.workers.worker import IngestionWorker

ADMIN_URL = os.environ.get(
    "TEST_DATABASE_URL",
    f"postgresql://dataatlas:{os.environ.get('PGPASSWORD', 'dataatlas')}"
    "@localhost:5432/dataatlas?sslmode=disable",
)
APP_URL = "postgresql://dataatlas_app:app-local-dev-only@localhost:5432/dataatlas?sslmode=disable"
APP_ROLE_PASSWORD = os.environ.get("POSTGRES_APP_PASSWORD", "app-local-dev-only")


@pytest.fixture(scope="module")
def quality_env():
    if not os.environ.get("APP_ENCRYPTION_KEY"):
        pytest.skip("APP_ENCRYPTION_KEY not set; integration env missing")

    schema = f"quality_demo_{uuid.uuid4().hex[:8]}"

    async def setup() -> None:
        admin = await asyncpg.connect(ADMIN_URL)
        try:
            await admin.execute(  # noqa: S608 - fixture
                f"CREATE SCHEMA {schema}; GRANT USAGE ON SCHEMA {schema} TO dataatlas_app"
            )
            await admin.execute(  # noqa: S608 - fixture
                f"""
                CREATE TABLE {schema}.payments (
                    id SERIAL PRIMARY KEY,
                    order_id INTEGER,
                    amount NUMERIC(10, 2),
                    status TEXT,
                    paid_at TIMESTAMPTZ DEFAULT now()
                )
                """
            )
            # Known defects: 2 null order_ids, 2 duplicate order refs,
            # 1 amount out of range, 1 unexpected status.
            await admin.execute(  # noqa: S608 - fixture
                f"""
                INSERT INTO {schema}.payments (order_id, amount, status) VALUES
                (1, 50.00, 'paid'), (2, 30.00, 'paid'), (3, 20.00, 'refunded'),
                (NULL, 10.00, 'paid'), (NULL, 70.00, 'paid'),
                (2, 15.00, 'pending'), (4, 999.00, 'paid'), (5, 25.00, 'unknown'),
                (6, 40.00, 'paid'), (7, 35.00, 'paid')
                """
            )
            await admin.execute(  # noqa: S608 - fixture
                f"GRANT SELECT ON ALL TABLES IN SCHEMA {schema} TO dataatlas_app"
            )
        finally:
            await admin.close()

    asyncio.run(setup())

    async def make_conn() -> asyncpg.Connection:
        return await asyncpg.connect(APP_URL)

    yield {"schema": schema, "conn_factory": make_conn}

    async def teardown() -> None:
        admin = await asyncpg.connect(ADMIN_URL)
        try:
            await admin.execute(  # noqa: S608 - fixture
                f"DROP SCHEMA IF EXISTS {schema} CASCADE"
            )
        finally:
            await admin.close()

    asyncio.run(teardown())


def make_check(check_id: str, ctype: str, params: dict, column: str | None = None,
               severity: str = "ERROR") -> dict:
    return {
        "id": check_id,
        "check_type": ctype,
        "target_column": column,
        "parameters": params,
        "severity": severity,
        "table_name": "payments",
        "schema_name": quality_env_schema(),
    }


_SCHEMA_HOLDER = {"schema": ""}


def quality_env_schema() -> str:
    return _SCHEMA_HOLDER["schema"]


@pytest.fixture(autouse=True)
def _bind_schema(quality_env: dict) -> None:
    _SCHEMA_HOLDER["schema"] = quality_env["schema"]


@pytest.mark.integration
async def test_structured_checks_detect_known_defects(quality_env: dict) -> None:
    conn = await quality_env["conn_factory"]()
    try:
        engine = QualityEngine(conn)

        # NOT NULL: 2 null order_ids.
        outcome = await engine.execute_check(
            make_check(str(uuid.uuid4()), "NOT_NULL", {}, column="order_id")
        )
        assert outcome.status == "FAILED"
        assert outcome.rows_failed == 2

        # UNIQUE: order_id 2 appears twice -> 1 surplus row.
        outcome = await engine.execute_check(
            make_check(str(uuid.uuid4()), "UNIQUE", {}, column="order_id")
        )
        assert outcome.status == "FAILED"
        assert outcome.rows_failed == 1

        # ACCEPTED VALUES: 'unknown' is not allowed.
        outcome = await engine.execute_check(
            make_check(
                str(uuid.uuid4()), "ACCEPTED_VALUES",
                {"values": ["paid", "refunded", "pending"]},
                column="status",
            )
        )
        assert outcome.status == "FAILED"
        assert outcome.rows_failed == 1

        # NUMERIC RANGE: 999 exceeds the maximum.
        outcome = await engine.execute_check(
            make_check(
                str(uuid.uuid4()), "NUMERIC_RANGE",
                {"min": 0, "max": 500}, column="amount",
            )
        )
        assert outcome.status == "FAILED"
        assert outcome.rows_failed == 1

        # REGEX: status must be lowercase letters; 'unknown' passes so 0.
        outcome = await engine.execute_check(
            make_check(
                str(uuid.uuid4()), "REGEX", {"pattern": "^[a-z]+$"},
                column="status",
            )
        )
        assert outcome.status == "PASSED"
        assert outcome.rows_failed == 0

        # Severity downgrade: failures become WARNING for low severity.
        outcome = await engine.execute_check(
            make_check(str(uuid.uuid4()), "NOT_NULL", {}, column="order_id",
                       severity="WARNING")
        )
        assert outcome.status == "WARNING"

        # ROW COUNT within bounds.
        outcome = await engine.execute_check(
            make_check(str(uuid.uuid4()), "ROW_COUNT", {"min": 5, "max": 20})
        )
        assert outcome.status == "PASSED"
        assert outcome.rows_evaluated == 10

        # REFERENTIAL INTEGRITY: order_id 7 has no matching parent.
        async def make_parent() -> None:
            admin = await asyncpg.connect(ADMIN_URL)
            try:
                await admin.execute(  # noqa: S608 - fixture
                    f"CREATE TABLE IF NOT EXISTS {quality_env_schema()}.orders "
                    f"(id INTEGER PRIMARY KEY)"
                )
                await admin.execute(  # noqa: S608 - fixture
                    f"INSERT INTO {quality_env_schema()}.orders VALUES (1),(2),(3),(4),(5),(6)"
                )
                await admin.execute(  # noqa: S608 - fixture
                    f"GRANT SELECT ON {quality_env_schema()}.orders TO dataatlas_app"
                )
            finally:
                await admin.close()

        await make_parent()
        outcome = await engine.execute_check(
            make_check(
                str(uuid.uuid4()), "REFERENTIAL_INTEGRITY",
                {"reference_table": "orders", "reference_column": "id"},
                column="order_id",
            )
        )
        assert outcome.status == "FAILED"
        assert outcome.rows_failed == 1
    finally:
        await conn.close()


@pytest.mark.integration
async def test_provenance_and_expected_values_recorded(quality_env: dict) -> None:
    conn = await quality_env["conn_factory"]()
    try:
        engine = QualityEngine(conn)
        outcome = await engine.execute_check(
            make_check(str(uuid.uuid4()), "NOT_NULL", {}, column="order_id")
        )
        assert outcome.actual["rows_failed"] == 2
        assert outcome.duration_ms >= 0
        assert outcome.status in ("FAILED", "WARNING")
    finally:
        await conn.close()


@pytest.mark.integration
async def test_custom_sql_safety_model(quality_env: dict) -> None:
    conn = await quality_env["conn_factory"]()
    try:
        engine = QualityEngine(conn)

        # Valid read-only query counts failures.
        outcome = await engine.execute_check(
            make_check(str(uuid.uuid4()), "CUSTOM_SQL",
                       {"sql": "SELECT order_id FROM payments WHERE order_id IS NULL"})
        )
        assert outcome.status == "FAILED"
        assert outcome.rows_failed == 2

        # Passing query.
        outcome = await engine.execute_check(
            make_check(str(uuid.uuid4()), "CUSTOM_SQL",
                       {"sql": "SELECT order_id FROM payments WHERE 1 = 0"})
        )
        assert outcome.status == "PASSED"

        # Rejected: multi-statement.
        outcome = await engine.execute_check(
            make_check(str(uuid.uuid4()), "CUSTOM_SQL",
                       {"sql": "SELECT 1; SELECT 2"})
        )
        assert outcome.status == "ERROR"

        # Rejected: INSERT root.
        outcome = await engine.execute_check(
            make_check(str(uuid.uuid4()), "CUSTOM_SQL",
                       {"sql": "INSERT INTO payments (order_id) VALUES (1)"})
        )
        assert outcome.status == "ERROR"

        # Rejected: DELETE wrapped in a CTE-ish shape.
        outcome = await engine.execute_check(
            make_check(str(uuid.uuid4()), "CUSTOM_SQL",
                       {"sql": "WITH x AS (SELECT 1) DELETE FROM payments"})
        )
        assert outcome.status == "ERROR"

        # Rejected: DROP.
        outcome = await engine.execute_check(
            make_check(str(uuid.uuid4()), "CUSTOM_SQL",
                       {"sql": "DROP TABLE payments"})
        )
        assert outcome.status == "ERROR"
    finally:
        await conn.close()


@pytest.mark.integration
async def test_worker_quality_run_end_to_end(quality_env: dict) -> None:
    """Worker path: seed checks in the catalog, run_quality executes them."""
    # Set up org/source/dataset/checks through the metadata database.

    schema = quality_env["schema"]
    org_id = str(uuid.uuid4())
    source_id = str(uuid.uuid4())
    dataset_id = str(uuid.uuid4())
    asset_id = str(uuid.uuid4())
    check_id = str(uuid.uuid4())

    key = bytes.fromhex(os.environ["APP_ENCRYPTION_KEY"])
    sealed = AESGCM(key).encrypt(
        b"\x06" * 12, json.dumps({"password": APP_ROLE_PASSWORD}).encode(), None
    )

    async def seed() -> None:
        conn = await asyncpg.connect(APP_URL)
        try:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)", org_id
                )
                await conn.execute(
                    "INSERT INTO organizations (id, name, slug) VALUES ($1, 'Q Org', $2)",
                    uuid.UUID(org_id), f"quality-org-{org_id[:8]}",
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
                    VALUES ($1, $2, 'postgres', 'Q Source', $3, 'ACTIVE', $4)
                    """,
                    uuid.UUID(source_id), uuid.UUID(org_id), f"quality-src-{source_id[:8]}",
                    json.dumps({"host": "localhost", "port": 5432, "database": "dataatlas",
                                "user": "dataatlas_app", "schemas": [schema]}),
                )
                await conn.execute(
                    """
                    INSERT INTO source_credentials
                        (data_source_id, organization_id, ciphertext, nonce)
                    VALUES ($1, $2, $3, $4)
                    """,
                    uuid.UUID(source_id), uuid.UUID(org_id), sealed, b"\x06" * 12,
                )
                await conn.execute(
                    """
                    INSERT INTO data_assets
                        (id, organization_id, data_source_id, asset_type, namespace, name)
                    VALUES ($1, $2, $3, 'TABLE', $4, 'payments')
                    """,
                    uuid.UUID(asset_id), uuid.UUID(org_id), uuid.UUID(source_id), schema,
                )
                await conn.execute(
                    """
                    INSERT INTO datasets
                        (id, asset_id, organization_id, row_count, row_count_method)
                    VALUES ($1, $2, $3, 10, 'sample')
                    """,
                    uuid.UUID(dataset_id), uuid.UUID(asset_id), uuid.UUID(org_id),
                )
                await conn.execute(
                    """
                    INSERT INTO quality_checks
                        (id, organization_id, dataset_id, name, check_type,
                         target_column, parameters, severity)
                    VALUES ($1, $2, $3, 'order_id not null', 'NOT_NULL',
                            'order_id', '{}', 'ERROR')
                    """,
                    uuid.UUID(check_id), uuid.UUID(org_id), uuid.UUID(dataset_id),
                )
        finally:
            await conn.close()

    await seed()

    worker = IngestionWorker()
    worker.settings.database_url = APP_URL
    worker._pool = await asyncpg.create_pool(  # noqa: SLF001 - test setup
        worker.settings.database_url, min_size=1, max_size=2
    )
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
                    SELECT id FROM ingestion_pipelines
                    WHERE data_source_id = $1 AND deleted_at IS NULL LIMIT 1
                    """,
                    uuid.UUID(source_id),
                )
                if pipeline is None:
                    pipeline = await conn.fetchval(
                        """
                        INSERT INTO ingestion_pipelines
                            (organization_id, data_source_id, name, pipeline_type, status)
                        VALUES ($1, $2, 'Q discovery', 'METADATA_DISCOVERY', 'ACTIVE')
                        RETURNING id
                        """,
                        uuid.UUID(org_id), uuid.UUID(source_id),
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
    try:
        summary = await worker.run_quality(run_id, org_id)
    finally:
        await worker.stop()

    assert summary["checks_executed"] == 1

    conn = await asyncpg.connect(APP_URL)
    try:
        async with conn.transaction():
            await conn.execute(
                "SELECT set_config('app.current_organization_id', $1, true)", org_id
            )
            result = await conn.fetchrow(
                """
                SELECT status, rows_failed FROM quality_results
                WHERE check_id = $1
                """,
                uuid.UUID(check_id),
            )
    finally:
        await conn.close()
    assert result is not None
    assert result["status"] == "FAILED"
    assert result["rows_failed"] == 2
