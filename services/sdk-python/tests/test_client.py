"""SDK client tests using an in-process HTTP transport."""

import httpx
import pytest

from dataatlas import AuthenticationError, DataAtlas, DataAtlasError


def make_client(handler: httpx.MockTransport) -> DataAtlas:
    client = DataAtlas(api_key="test-key", base_url="http://testserver")
    client._http = httpx.Client(
        base_url="http://testserver",
        transport=handler,
        headers={"Authorization": "Bearer test-key"},
    )
    return client


def test_health_returns_payload() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/healthz"
        assert request.headers["Authorization"] == "Bearer test-key"
        return httpx.Response(200, json={"status": "ok"})

    with make_client(httpx.MockTransport(handler)) as client:
        assert client.health() == {"status": "ok"}


def test_error_status_raises_dataatlas_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(500, json={"error": "boom"})

    with make_client(httpx.MockTransport(handler)) as client:
        with pytest.raises(DataAtlasError):
            client.health()


def test_unauthorized_raises_authentication_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(401, json={"error": "invalid api key"})

    with make_client(httpx.MockTransport(handler)) as client:
        with pytest.raises(AuthenticationError):
            client.health()


def test_network_failure_raises_dataatlas_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("connection refused")

    with make_client(httpx.MockTransport(handler)) as client:
        with pytest.raises(DataAtlasError):
            client.version()
