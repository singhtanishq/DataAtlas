"""Health endpoint tests for the ingestion engine."""

from fastapi.testclient import TestClient

from app.main import create_app


def test_healthz_reports_ok() -> None:
    client = TestClient(create_app())
    response = client.get("/healthz")
    assert response.status_code == 200
    body = response.json()
    assert body["status"] == "ok"
    assert body["service"] == "ingestion-engine"


def test_readyz_reports_ready() -> None:
    client = TestClient(create_app())
    response = client.get("/readyz")
    assert response.status_code == 200
    assert response.json()["status"] == "ready"


def test_version_reports_service_name() -> None:
    client = TestClient(create_app())
    response = client.get("/version")
    assert response.status_code == 200
    assert response.json()["service"] == "ingestion-engine"
