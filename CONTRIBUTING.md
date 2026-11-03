# Contributing to DataAtlas

Thank you for considering a contribution to DataAtlas!

## Development Setup

Prerequisites:

- macOS / Linux (Apple Silicon and x86_64 supported)
- Node.js >= 22 and pnpm >= 10
- Go >= 1.25
- Python >= 3.13 with [uv](https://docs.astral.sh/uv/)
- Docker and Docker Compose

```bash
# 1. Install frontend dependencies
pnpm install

# 2. Start local infrastructure (PostgreSQL, Redis, NATS, MinIO, ...)
make infra-up

# 3. Run database migrations
make migrate

# 4. Seed deterministic demo data
make seed

# 5. Start services in development mode
make dev
```

## Repository Layout

```text
apps/web                 Next.js dashboard (frontend)
services/control-plane   Go API / control plane
services/ingestion-engine  Python metadata ingestion & quality engine
services/sdk-python      Official Python SDK
infra/                   Docker Compose and service configuration
docs/                    Architecture, database, API and security documentation
examples/                Sample data, dbt project, OpenLineage fixtures
```

## Engineering Standards

All contributions must pass:

```bash
make lint    # ESLint + TypeScript (web), go vet (control plane), ruff + mypy (Python)
make test    # go test, pytest, vitest
```

- **Go:** `gofmt`, `go vet`, race detector; table-driven tests for new logic.
- **Python:** `ruff` formatting and linting, strict `mypy` for new modules,
  `pytest` for tests.
- **TypeScript:** strict mode, ESLint clean, no `any` in new code.

## Commit Messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

```text
feat(catalog): implement dataset discovery
fix(lineage): handle out-of-order lifecycle events
docs(api): document freshness endpoints
test(tenancy): add cross-organization isolation tests
```

## Pull Requests

1. Fork the repository and create a feature branch from `main`.
2. Keep changes focused; one logical change per PR.
3. Add or update tests for any behavior change.
4. Update documentation for user-facing changes.
5. Ensure `make lint` and `make test` pass locally.
6. Never commit secrets, credentials, `.env` files, or raw dataset exports.

## Reporting Bugs

Open a GitHub issue with reproduction steps, expected behavior, and the
component involved. For security issues, see `SECURITY.md`.
