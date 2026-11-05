"""Ingestion worker: consumes connector jobs from NATS.

The worker is the only component that decrypts source credentials. Secrets
never travel through the broker: workers receive a source id, load the
encrypted credentials from PostgreSQL, and decrypt locally.

Two consumption paths:
- validate_connection requests (core NATS request/reply)
- durable discovery jobs (JetStream pull consumer with explicit acks)
"""

from __future__ import annotations

import asyncio
import json
import logging
import time
import uuid
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
DISCOVERY_SUBJECT = "ingestion.discover"
PROFILE_SUBJECT = "ingestion.profile"
QUALITY_SUBJECT = "ingestion.quality"
INGESTION_STREAM = "INGESTION"
WORKER_QUEUE = "ingestion-workers"
WORKER_DURABLE = "ingestion-workers"


class IngestionWorker:
    """NATS worker for connector operations."""

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

    async def run_discovery_loop(self, stop: asyncio.Event) -> None:
        """Consume durable discovery jobs until stop is set."""
        js = self._nc.jetstream()
        try:
            await js.add_stream(name=INGESTION_STREAM, subjects=["ingestion.>"])
        except Exception:  # noqa: BLE001 - stream already exists
            logger.debug("stream exists", extra={"stream": INGESTION_STREAM})
        try:
            await js.add_consumer(
                INGESTION_STREAM,
                durable=WORKER_DURABLE,
                ack_policy="explicit",
                max_deliver=5,
                ack_wait=300,
            )
        except Exception:  # noqa: BLE001 - consumer already exists
            logger.debug("consumer exists", extra={"durable": WORKER_DURABLE})
        sub = await js.pull_subscribe(
            DISCOVERY_SUBJECT, durable=WORKER_DURABLE, stream=INGESTION_STREAM
        )
        profile_sub = await js.pull_subscribe(
            PROFILE_SUBJECT, durable=WORKER_DURABLE + "-profile", stream=INGESTION_STREAM
        )
        quality_sub = await js.pull_subscribe(
            QUALITY_SUBJECT, durable=WORKER_DURABLE + "-quality", stream=INGESTION_STREAM
        )
        logger.info(
            "consumers ready",
            extra={"subjects": [DISCOVERY_SUBJECT, PROFILE_SUBJECT, QUALITY_SUBJECT]},
        )
        while not stop.is_set():
            try:
                msgs = await sub.fetch(1, timeout=5)
            except TimeoutError:
                msgs = []
            for msg in msgs:
                await self._handle_discovery(msg)
            try:
                pmsgs = await profile_sub.fetch(1, timeout=1)
            except TimeoutError:
                pmsgs = []
            for msg in pmsgs:
                await self._handle_profile(msg)
            try:
                qmsgs = await quality_sub.fetch(1, timeout=1)
            except TimeoutError:
                qmsgs = []
            for msg in qmsgs:
                await self._handle_quality(msg)

    async def stop(self) -> None:
        if self._nc is not None:
            await self._nc.drain()
        if self._pool is not None:
            await self._pool.close()

    # -- shared source loading ----------------------------------------------

    async def _load_source(
        self, source_id: str, organization_id: str
    ) -> tuple[str, dict[str, Any], dict[str, Any]] | None:
        """Load connector key, config and decrypted credentials for a source.

        The tenant context is transaction-local by design; the read runs in
        one transaction so RLS sees it and it cannot leak into later reuses
        of this pooled connection.
        """
        assert self._pool is not None
        async with self._pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                row = await conn.fetchrow(
                    """
                    SELECT ds.connector_key, ds.config, sc.ciphertext, sc.nonce
                    FROM data_sources ds
                    JOIN source_credentials sc ON sc.data_source_id = ds.id
                    WHERE ds.id = $1 AND ds.deleted_at IS NULL
                    """,
                    source_id,
                )
        if row is None:
            return None
        try:
            credentials = self.cipher.decrypt_json(
                bytes(row["ciphertext"]), bytes(row["nonce"])
            )
        except Exception as exc:
            logger.warning("credential decryption failed", extra={"error": str(exc)})
            return None
        raw_config = row["config"]
        if isinstance(raw_config, dict):
            config: dict[str, Any] = raw_config
        else:
            config = json.loads(raw_config or "{}")
        return row["connector_key"], config, credentials

    # -- connection validation (request/reply) -------------------------------

    async def _handle_validate(self, msg: Msg) -> None:
        try:
            request = json.loads(msg.data)
        except json.JSONDecodeError:
            await msg.respond(b'{"ok": false, "message": "invalid request payload"}')
            return
        source_id = request.get("source_id")
        organization_id = request.get("organization_id")
        if not source_id or not organization_id:
            await msg.respond(
                b'{"ok": false, "message": "source_id and organization_id required"}'
            )
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
        started = time.monotonic()
        loaded = await self._load_source(source_id, organization_id)
        if loaded is None:
            return ConnectorResult(
                ok=False, message="source not found or credentials unreadable"
            )
        connector_key, config, credentials = loaded

        try:
            connector_cls = get_connector_class(connector_key)
        except LookupError as exc:
            return ConnectorResult(ok=False, message=str(exc))

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

    # -- discovery jobs (JetStream durable) -----------------------------------

    async def _handle_discovery(self, msg: Msg) -> None:
        """Process one durable discovery job end to end."""
        try:
            request = json.loads(msg.data)
        except json.JSONDecodeError:
            await msg.ack()
            return
        run_id = request.get("run_id")
        source_id = request.get("source_id")
        organization_id = request.get("organization_id")
        if not (run_id and source_id and organization_id):
            await msg.ack()
            return

        await self._set_run_status(run_id, organization_id, "RUNNING")
        try:
            summary = await self.run_discovery(source_id, organization_id)
            await self._finish_run(run_id, organization_id, "SUCCEEDED", None, summary)
            await msg.ack()
            logger.info(
                "discovery completed",
                extra={"run_id": run_id, "summary": summary.as_dict()},
            )
        except Exception as exc:
            logger.exception("discovery failed", extra={"run_id": run_id})
            await self._finish_run(run_id, organization_id, "FAILED", str(exc)[:500], None)
            # Requeue for redelivery; the consumer caps at max_deliver.
            await msg.nak(delay=10)

    async def run_discovery(self, source_id: str, organization_id: str) -> Any:
        """Full metadata discovery + catalog application for one source."""
        from app.connectors.postgres import PostgreSQLConnector
        from app.discovery.postgres_discovery import PostgreSQLDiscovery
        from app.metadata.apply import ApplySummary, CatalogApplier

        loaded = await self._load_source(source_id, organization_id)
        if loaded is None:
            raise RuntimeError("source not found or credentials unreadable")
        connector_key, config, credentials = loaded
        connector_cls = get_connector_class(connector_key)
        connector = connector_cls(config=config, credentials=credentials)

        if not isinstance(connector, PostgreSQLConnector):
            raise RuntimeError(f"connector {connector_key} does not support discovery yet")

        allowed_schemas = config.get("schemas") or None
        discovery = PostgreSQLDiscovery(
            connector._connection_kwargs(), allowed_schemas=allowed_schemas
        )
        datasets = await discovery.discover_datasets()
        for ds in datasets:
            ds.columns = await discovery.discover_columns(ds.namespace, ds.name)
        fk_targets = await discovery.foreign_key_targets()

        database_name: str | None = config.get("database")
        applier = CatalogApplier(self._pool)
        summary: ApplySummary = await applier.apply_discovery(
            organization_id, source_id, database_name, datasets, fk_targets,
            database_name=database_name,
        )
        return summary

    async def _handle_profile(self, msg: Msg) -> None:
        """Profile one dataset and store the results."""
        try:
            request = json.loads(msg.data)
        except json.JSONDecodeError:
            await msg.ack()
            return
        run_id = request.get("run_id")
        dataset_id = request.get("dataset_id")
        organization_id = request.get("organization_id")
        if not (run_id and dataset_id and organization_id):
            await msg.ack()
            return

        try:
            await self.profile_dataset(run_id, dataset_id, organization_id)
            await msg.ack()
            logger.info("profiling completed", extra={"run_id": run_id})
        except Exception as exc:
            logger.exception("profiling failed", extra={"run_id": run_id})
            await self._finish_run(run_id, organization_id, "FAILED", str(exc)[:500], None)
            await msg.nak(delay=10)

    async def profile_dataset(self, run_id: str, dataset_id: str, organization_id: str) -> None:
        """Profile one catalog dataset and persist results."""
        from app.profiling.profiler import DatasetProfiler

        assert self._pool is not None
        async with self._pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                row = await conn.fetchrow(
                    """
                    SELECT d.id::text, a.namespace, a.name, ds.config AS source_config,
                           sc.ciphertext, sc.nonce
                    FROM datasets d
                    JOIN data_assets a ON a.id = d.asset_id
                    JOIN data_sources ds ON ds.id = a.data_source_id
                    JOIN source_credentials sc ON sc.data_source_id = ds.id
                    WHERE d.id = $1 AND d.deleted_at IS NULL
                    """,
                    dataset_id,
                )
        if row is None:
            raise RuntimeError("dataset or source credentials not found")
        credentials = self.cipher.decrypt_json(bytes(row["ciphertext"]), bytes(row["nonce"]))
        raw_config = row["source_config"]
        config = raw_config if isinstance(raw_config, dict) else json.loads(raw_config or "{}")

        # Connect to the source database with read-only session defaults.
        from app.connectors.postgres import PostgreSQLConnector

        connector = PostgreSQLConnector(config=config, credentials=credentials)
        conn_kwargs = connector._connection_kwargs()  # noqa: SLF001 - internal reuse
        # Columns come from the catalog (metadata database), profiling from
        # the source database; the two never mix connections. The catalog
        # read is transaction-bound to the tenant for RLS.
        async with self._pool.acquire() as meta:
            async with meta.transaction():
                await meta.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                columns = await meta.fetch(
                    """
                    SELECT c.name, c.data_type FROM dataset_columns c
                    WHERE c.dataset_id = $1 AND c.status = 'ACTIVE'
                    ORDER BY c.ordinal
                    """,
                    uuid.UUID(dataset_id),
                )
        src = await asyncpg.connect(**conn_kwargs)
        try:
            profiler = DatasetProfiler(src)
            result = await profiler.profile_dataset(
                row["namespace"], row["name"], columns
            )
        finally:
            await src.close()
        result.dataset_id = dataset_id

        await self._store_profile(run_id, organization_id, result)

    async def _store_profile(self, run_id: str, organization_id: str, result: Any) -> None:
        assert self._pool is not None
        snapshot = json.dumps(result.as_dict(), default=str)
        methods = {"row_count": result.row_count_method or "inferred"}
        async with self._pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                # The profiling run is the analytical record; it links back to
                # the operational ingestion run via ingestion_run_id.
                profiling_run_id = await conn.fetchval(
                    """
                    INSERT INTO profiling_runs
                        (dataset_id, organization_id, ingestion_run_id, status,
                         scan_strategy, scanned_rows, finished_at, duration_ms)
                    VALUES ($1, $2, $3, 'SUCCEEDED', 'SAMPLE', $4, now(), $5)
                    RETURNING id
                    """,
                    uuid.UUID(result.dataset_id), uuid.UUID(organization_id),
                    uuid.UUID(run_id), result.row_count, result.duration_ms,
                )
                snapshot_id = await conn.fetchval(
                    """
                    INSERT INTO profile_snapshots
                        (profiling_run_id, dataset_id, organization_id, row_count,
                         column_count, metrics, metric_method)
                    VALUES ($1, $2, $3, $4, $5, $6, $7)
                    RETURNING id
                    """,
                    profiling_run_id, uuid.UUID(result.dataset_id),
                    uuid.UUID(organization_id), result.row_count,
                    result.column_count, snapshot, json.dumps(methods),
                )
                for col in result.columns:
                    col_row = await conn.fetchval(
                        """
                        SELECT id FROM dataset_columns
                        WHERE dataset_id = $1 AND name = $2 AND status = 'ACTIVE'
                        """,
                        uuid.UUID(result.dataset_id), col.name,
                    )
                    if col_row is None:
                        continue
                    for m in col.metrics:
                        value = m["value"]
                        await conn.execute(
                            """
                            INSERT INTO column_profiles
                                (profiling_run_id, dataset_column_id, organization_id,
                                 metric_name, metric_value, metric_value_text, value_type)
                            VALUES ($1, $2, $3, $4, $5, $6, $7)
                            """,
                            profiling_run_id, col_row, uuid.UUID(organization_id),
                            m["metric_name"],
                            float(value) if isinstance(value, (int, float)) else None,
                            (
                                None
                                if isinstance(value, (int, float))
                                else json.dumps(value, default=str)
                            ),
                            m["value_type"].upper(),
                        )
        logger.info(
            "profile stored",
            extra={"run_id": run_id, "snapshot_id": str(snapshot_id)},
        )

    async def _handle_quality(self, msg: Msg) -> None:
        """Execute a quality run: all enabled checks for the organization."""
        try:
            request = json.loads(msg.data)
        except json.JSONDecodeError:
            await msg.ack()
            return
        run_id = request.get("run_id")
        organization_id = request.get("organization_id")
        if not (run_id and organization_id):
            await msg.ack()
            return

        try:
            summary = await self.run_quality(run_id, organization_id)
            await self._finish_run(run_id, organization_id, "SUCCEEDED", None, summary)
            await msg.ack()
            logger.info("quality run completed", extra={"run_id": run_id})
        except Exception as exc:
            logger.exception("quality run failed", extra={"run_id": run_id})
            await self._finish_run(run_id, organization_id, "FAILED", str(exc)[:500], None)
            await msg.nak(delay=10)

    async def run_quality(self, run_id: str, organization_id: str) -> dict[str, Any]:
        """Execute every enabled quality check in the organization."""
        from app.quality.engine import QualityEngine

        assert self._pool is not None
        async with self._pool.acquire() as meta:
            async with meta.transaction():
                await meta.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                checks = await meta.fetch(
                    """
                    SELECT qc.id::text, qc.check_type, qc.target_column,
                           qc.parameters, qc.severity, qc.dataset_id::text,
                           a.name AS table_name, a.namespace AS schema_name,
                           ds.config AS source_config, sc.ciphertext, sc.nonce
                    FROM quality_checks qc
                    JOIN datasets d ON d.id = qc.dataset_id
                    JOIN data_assets a ON a.id = d.asset_id
                    JOIN data_sources ds ON ds.id = a.data_source_id
                    JOIN source_credentials sc ON sc.data_source_id = ds.id
                    WHERE qc.enabled = true AND qc.deleted_at IS NULL
                      AND d.deleted_at IS NULL
                    """
                )
        if not checks:
            return {"checks_executed": 0}

        # Group checks by source credentials (each dataset has one source).
        by_source: dict[bytes, list[dict[str, Any]]] = {}
        for row in checks:
            key = bytes(row["ciphertext"])
            by_source.setdefault(key, []).append(dict(row))

        executed = 0
        passed = 0
        failed = 0
        errored = 0

        # The analytical run record lives in quality_runs; results reference it.
        async with self._pool.acquire() as meta:
            async with meta.transaction():
                await meta.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                quality_run_id = await meta.fetchval(
                    """
                    INSERT INTO quality_runs (organization_id, trigger, status, started_at)
                    VALUES ($1, 'API', 'RUNNING', now())
                    RETURNING id
                    """,
                    uuid.UUID(organization_id),
                )

        for checks_for_source in by_source.values():
            first = checks_for_source[0]
            credentials = self.cipher.decrypt_json(
                bytes(first["ciphertext"]), bytes(first["nonce"])
            )
            raw_config = first["source_config"]
            config = raw_config if isinstance(raw_config, dict) else json.loads(raw_config or "{}")
            from app.connectors.postgres import PostgreSQLConnector

            connector = PostgreSQLConnector(config=config, credentials=credentials)
            src = await asyncpg.connect(**connector._connection_kwargs())  # noqa: SLF001
            try:
                engine = QualityEngine(src)
                for check in checks_for_source:
                    outcome = await engine.execute_check(check)
                    await self._store_quality_result(
                        quality_run_id, organization_id, check, outcome
                    )
                    if outcome.status == "FAILED":
                        await self._raise_alert(
                            organization_id,
                            alert_type="QUALITY_CHECK_FAILURE",
                            severity=str(check.get("severity") or "ERROR"),
                            title=f"Quality check failed: {check.get('name', outcome.check_id)}",
                            summary=(
                                f"{outcome.rows_failed} of {outcome.rows_evaluated} rows "
                                f"failed check {check.get('name', outcome.check_id)}"
                            ),
                            entity_type="dataset",
                            entity_id=check.get("dataset_id"),
                            dedupe_key=f"quality:{outcome.check_id}",
                        )
                    executed += 1
                    if outcome.status == "PASSED":
                        passed += 1
                    elif outcome.status == "ERROR":
                        errored += 1
                    else:
                        failed += 1
            finally:
                await src.close()

        async with self._pool.acquire() as meta:
            async with meta.transaction():
                await meta.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                await meta.execute(
                    """
                    UPDATE quality_runs SET status = 'SUCCEEDED', finished_at = now(),
                        duration_ms = GREATEST(
                            (EXTRACT(EPOCH FROM (now() - started_at)) * 1000)::bigint, 0)
                    WHERE id = $1
                    """,
                    quality_run_id,
                )
        return {
            "checks_executed": executed,
            "checks_passed": passed,
            "checks_failed": failed,
            "checks_errored": errored,
            "quality_run_id": str(quality_run_id),
        }

    async def _raise_alert(
        self, organization_id: str, alert_type: str, severity: str,
        title: str, summary: str, entity_type: str | None,
        entity_id: str | None, dedupe_key: str,
    ) -> None:
        """Create an alert unless an unresolved duplicate exists."""
        assert self._pool is not None
        async with self._pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                await conn.execute(
                    """
                    INSERT INTO alerts
                        (organization_id, alert_type, severity, title, summary,
                         entity_type, entity_id, dedupe_key)
                    VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
                    ON CONFLICT (organization_id, dedupe_key)
                    WHERE dedupe_key IS NOT NULL AND status <> 'RESOLVED'
                    DO UPDATE SET updated_at = now(), summary = EXCLUDED.summary
                    """,
                    uuid.UUID(organization_id), alert_type, severity, title,
                    summary, entity_type,
                    uuid.UUID(entity_id) if entity_id else None, dedupe_key,
                )

    async def _store_quality_result(
        self, quality_run_id: uuid.UUID, organization_id: str,
        check: dict[str, Any], outcome: Any,
    ) -> None:
        assert self._pool is not None
        actual = json.dumps(outcome.actual, default=str)
        async with self._pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                await conn.execute(
                    """
                    INSERT INTO quality_results
                        (quality_run_id, check_id, dataset_id, organization_id,
                         status, rows_evaluated, rows_failed, actual_value,
                         error_message, duration_ms)
                    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
                    """,
                    quality_run_id, uuid.UUID(outcome.check_id),
                    uuid.UUID(check["dataset_id"]), uuid.UUID(organization_id),
                    outcome.status, outcome.rows_evaluated, outcome.rows_failed,
                    actual, outcome.error_message, outcome.duration_ms,
                )

    async def _set_run_status(self, run_id: str, organization_id: str, status: str) -> None:
        assert self._pool is not None
        async with self._pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                await conn.execute(
                    """
                    UPDATE ingestion_runs SET status = $2,
                        started_at = COALESCE(started_at, now())
                    WHERE id = $1
                    """,
                    run_id, status,
                )

    async def _finish_run(
        self, run_id: str, organization_id: str, status: str,
        error_message: str | None, summary: Any,
    ) -> None:
        assert self._pool is not None
        progress = json.dumps(summary.as_dict()) if summary else "{}"
        async with self._pool.acquire() as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.current_organization_id', $1, true)",
                    organization_id,
                )
                await conn.execute(
                    """
                    UPDATE ingestion_runs SET status = $2, finished_at = now(),
                        duration_ms = GREATEST(
                            (EXTRACT(EPOCH FROM (now() - started_at)) * 1000)::bigint, 0),
                        error_message = $3, progress = $4
                    WHERE id = $1
                    """,
                    run_id, status, error_message, progress,
                )


async def main() -> None:  # pragma: no cover - process entrypoint
    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s"
    )
    worker = IngestionWorker()
    await worker.start()
    stop = asyncio.Event()
    loop_task = asyncio.create_task(worker.run_discovery_loop(stop))
    try:
        await stop.wait()
    except (KeyboardInterrupt, asyncio.CancelledError):
        pass
    finally:
        stop.set()
        loop_task.cancel()
        await worker.stop()


if __name__ == "__main__":  # pragma: no cover
    asyncio.run(main())
