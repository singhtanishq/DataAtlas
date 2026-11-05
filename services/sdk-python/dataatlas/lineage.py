"""Lineage and dataset operations for the DataAtlas SDK."""

from __future__ import annotations

import uuid
from datetime import UTC
from typing import Any

from dataatlas.client import DataAtlas, DataAtlasError


def _new_run_id() -> str:
    return str(uuid.uuid4())


class LineageResource:
    """Publish OpenLineage-compatible run events."""

    def __init__(self, client: DataAtlas) -> None:
        self._client = client

    def emit(
        self,
        job: str,
        namespace: str = "default",
        inputs: list[str] | None = None,
        outputs: list[str] | None = None,
        event_type: str = "COMPLETE",
        run_id: str | None = None,
    ) -> dict[str, Any]:
        """Emit one lineage run event.

        Args:
            job: job name (e.g. ``daily_customer_pipeline``).
            namespace: job namespace.
            inputs: input dataset names as ``namespace.name`` strings.
            outputs: output dataset names as ``namespace.name`` strings.
            event_type: START, COMPLETE, FAIL or ABORT.
            run_id: optional external run id; generated when omitted.

        Returns:
            The processed-event payload from the API.

        Raises:
            DataAtlasError: on API errors (invalid event, auth, network).
        """
        run_id = run_id or _new_run_id()

        def to_datasets(names: list[str] | None) -> list[dict[str, str]]:
            datasets: list[dict[str, str]] = []
            for name in names or []:
                if "." not in name:
                    raise DataAtlasError(
                        f"dataset {name!r} must be 'namespace.name'"
                    )
                ns, _, ds = name.partition(".")
                datasets.append({"namespace": ns, "name": ds})
            return datasets

        from datetime import datetime

        event = {
            "eventType": event_type,
            "eventTime": datetime.now(UTC).isoformat(),
            "producer": "https://github.com/singhtanishq/DataAtlas python-sdk",
            "schemaURL": "https://openlineage.io/spec/2-0-2/OpenLineage.json",
            "run": {"runId": run_id},
            "job": {"namespace": namespace, "name": job},
            "inputs": to_datasets(inputs),
            "outputs": to_datasets(outputs),
        }
        result: dict[str, Any] = self._client._request(
            "POST", "/v1/lineage/openlineage", json_body=event
        )
        return result


class DatasetsResource:
    """Read and annotate catalog datasets."""

    def __init__(self, client: DataAtlas) -> None:
        self._client = client

    def describe(self, dataset_id: str, description: str) -> dict[str, Any]:
        """Set a dataset's business description (DATA_ENGINEER role or above)."""
        result: dict[str, Any] = self._client._request(
            "PATCH", f"/v1/catalog/datasets/{dataset_id}",
            json_body={"description": description},
        )
        return result

    def list(self, query: str = "", limit: int = 25) -> list[dict[str, Any]]:
        """Search catalog datasets."""
        result: dict[str, Any] = self._client._request(
            "GET", f"/v1/catalog/datasets?q={query}&limit={limit}"
        )
        datasets: list[dict[str, Any]] = result.get("datasets", [])
        return datasets

    def get(self, dataset_id: str) -> dict[str, Any]:
        """Fetch full dataset details."""
        result: dict[str, Any] = self._client._request(
            "GET", f"/v1/catalog/datasets/{dataset_id}"
        )
        details: dict[str, Any] = result.get("dataset", {})
        return details
