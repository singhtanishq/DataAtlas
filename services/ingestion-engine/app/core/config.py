"""Ingestion engine runtime configuration loaded from environment variables."""

from functools import lru_cache

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    """Environment-driven settings for the ingestion engine."""

    model_config = SettingsConfigDict(env_file=".env", env_file_encoding="utf-8", extra="ignore")

    app_env: str = "development"
    ingestion_port: int = 8090
    log_level: str = "info"

    database_url: str = "postgresql://dataatlas:dataatlas@localhost:5432/dataatlas"
    redis_url: str = "redis://localhost:6379/0"
    nats_url: str = "nats://localhost:4222"

    # Connector safety defaults. All external access is read-only.
    connector_connect_timeout_seconds: float = 10.0
    connector_query_timeout_seconds: float = 30.0
    connector_max_rows: int = 100_000
    profiler_sample_rows: int = 100_000
    profiler_row_limit: int = 1_000_000


@lru_cache
def get_settings() -> Settings:
    """Return the cached process settings."""
    return Settings()
