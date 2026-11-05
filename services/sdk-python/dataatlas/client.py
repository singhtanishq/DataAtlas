"""HTTP client for the DataAtlas API."""

from typing import Any

import httpx

_DEFAULT_TIMEOUT_SECONDS = 30.0


class DataAtlasError(Exception):
    """Base error for all DataAtlas SDK failures."""

    def __init__(self, message: str, status_code: int | None = None) -> None:
        super().__init__(message)
        self.status_code = status_code


class AuthenticationError(DataAtlasError):
    """Raised when API credentials are rejected (HTTP 401/403)."""


class DataAtlas:
    """Client for the DataAtlas versioned REST API.

    Example:
        client = DataAtlas(api_key="...", base_url="http://localhost:8080")
        health = client.health()
    """

    def __init__(
        self,
        api_key: str = "",
        base_url: str = "http://localhost:8080",
        timeout: float = _DEFAULT_TIMEOUT_SECONDS,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._http = httpx.Client(
            base_url=self._base_url,
            timeout=timeout,
            headers=self._build_headers(api_key),
        )

    @staticmethod
    def _build_headers(api_key: str) -> dict[str, str]:
        headers = {
            "Accept": "application/json",
            "User-Agent": "dataatlas-sdk/0.1.0",
        }
        if api_key:
            headers["Authorization"] = f"Bearer {api_key}"
        return headers

    def _request(self, method: str, path: str, json_body: Any = None) -> Any:
        try:
            response = self._http.request(method, path, json=json_body)
        except httpx.HTTPError as exc:
            raise DataAtlasError(f"request to {path} failed: {exc}") from exc
        if response.status_code in (401, 403):
            raise AuthenticationError(
                "authentication rejected by DataAtlas API",
                status_code=response.status_code,
            )
        if response.status_code >= 400:
            raise DataAtlasError(
                f"DataAtlas API returned {response.status_code} for {path}",
                status_code=response.status_code,
            )
        if not response.content:
            return None
        return response.json()

    # -- resource namespaces -----------------------------------------------

    @property
    def lineage(self) -> Any:
        """Lineage event operations."""
        from dataatlas.lineage import LineageResource

        return LineageResource(self)

    @property
    def datasets(self) -> Any:
        """Catalog dataset operations."""
        from dataatlas.lineage import DatasetsResource

        return DatasetsResource(self)

    def health(self) -> dict[str, Any]:
        """Return the control-plane health payload."""
        result: dict[str, Any] = self._request("GET", "/healthz")
        return result

    def version(self) -> dict[str, Any]:
        """Return the control-plane version payload."""
        result: dict[str, Any] = self._request("GET", "/version")
        return result

    def close(self) -> None:
        """Release the underlying HTTP connection pool."""
        self._http.close()

    def __enter__(self) -> "DataAtlas":
        return self

    def __exit__(self, *exc_info: object) -> None:
        self.close()
