"""FastAPI application entrypoint for the DataAtlas ingestion engine."""

from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI

from app.core.config import get_settings

_DESCRIPTION = (
    "Metadata ingestion, profiling and quality execution engine for DataAtlas. "
    "This service reads metadata from connected data systems; it never writes "
    "to external sources."
)


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncIterator[None]:
    """Manage engine startup and shutdown hooks."""
    settings = get_settings()
    app.state.settings = settings
    yield


def create_app() -> FastAPI:
    """Build the ingestion engine FastAPI application."""
    settings = get_settings()
    app = FastAPI(
        title="DataAtlas Ingestion Engine",
        description=_DESCRIPTION,
        version="0.1.0",
        lifespan=lifespan,
    )

    @app.get("/healthz", tags=["system"])
    async def healthz() -> dict[str, Any]:
        return {
            "status": "ok",
            "service": "ingestion-engine",
            "env": settings.app_env,
        }

    @app.get("/readyz", tags=["system"])
    async def readyz() -> dict[str, Any]:
        # Dependency probes (database, NATS) are wired in later milestones.
        return {"status": "ready", "checks": {}}

    @app.get("/version", tags=["system"])
    async def version() -> dict[str, str]:
        return {"service": "ingestion-engine", "version": "0.1.0"}

    return app


app = create_app()
