"""Tests for the lineage and datasets resources."""

import httpx
import pytest

from dataatlas import DataAtlas, DataAtlasError


def make_client(handler: httpx.MockTransport) -> DataAtlas:
    client = DataAtlas(api_key="k", base_url="http://testserver")
    client._http = httpx.Client(
        base_url="http://testserver", transport=handler,
        headers={"Authorization": "Bearer k"},
    )
    return client


def test_emit_builds_openlineage_event() -> None:
    captured = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["path"] = request.url.path
        captured["body"] = request.read()
        return httpx.Response(200, json={"status": "processed"})

    with make_client(httpx.MockTransport(handler)) as client:
        result = client.lineage.emit(
            job="daily_customer_pipeline",
            namespace="analytics",
            inputs=["raw.customers"],
            outputs=["analytics.customer_metrics"],
        )
    assert result == {"status": "processed"}
    assert captured["path"] == "/v1/lineage/openlineage"
    import json
    event = json.loads(captured["body"])
    assert event["eventType"] == "COMPLETE"
    assert event["job"] == {"namespace": "analytics", "name": "daily_customer_pipeline"}
    assert event["inputs"] == [{"namespace": "raw", "name": "customers"}]
    assert event["outputs"] == [{"namespace": "analytics", "name": "customer_metrics"}]
    assert len(event["run"]["runId"]) == 36  # uuid


def test_emit_rejects_invalid_dataset_names() -> None:
    with make_client(httpx.MockTransport(lambda r: httpx.Response(200))) as client:
        with pytest.raises(DataAtlasError):
            client.lineage.emit(job="j", inputs=["no-dot-name"])


def test_describe_sends_patch() -> None:
    captured = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["method"] = request.method
        captured["path"] = request.url.path
        return httpx.Response(200, json={"status": "updated"})

    with make_client(httpx.MockTransport(handler)) as client:
        client.datasets.describe("ds-1", description="Daily customer metrics.")
    assert captured["method"] == "PATCH"
    assert captured["path"] == "/v1/catalog/datasets/ds-1"


def test_list_and_get_datasets() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/v1/catalog/datasets":
            return httpx.Response(200, json={"datasets": [{"id": "1", "name": "t"}]})
        return httpx.Response(200, json={"dataset": {"id": "1"}})

    with make_client(httpx.MockTransport(handler)) as client:
        assert client.datasets.list("q")[0]["name"] == "t"
        assert client.datasets.get("1")["id"] == "1"
