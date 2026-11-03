<div align="center">

# DataAtlas

**The Enterprise Data Intelligence and Observability Platform**

*Discover. Understand. Trust. Govern.*

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Python](https://img.shields.io/badge/Python-3.13+-3776AB?logo=python&logoColor=white)](https://www.python.org)
[![Next.js](https://img.shields.io/badge/Next.js-16-black?logo=next.js)](https://nextjs.org)

</div>

---

## What is DataAtlas?

DataAtlas is an enterprise data intelligence platform that continuously
discovers, catalogs, profiles, monitors and governs an organization's data
assets. It provides a single, continuously updated intelligence layer that
understands your data ecosystem — where every dataset lives, what it contains,
who owns it, where it came from, how fresh it is, and whether it can be
trusted.

Modern enterprises spread data across PostgreSQL databases, data warehouses,
object storage, CSV/Parquet files, dbt projects, ETL pipelines and BI
dashboards. Nobody can answer basic questions anymore: *Which datasets exist?
Which columns contain sensitive information? What breaks if this table
changes? Why is this dashboard wrong?* DataAtlas answers those questions with
evidence, not guesswork.

## Status

> **⚠️ Under active development.** This repository is being built out
> milestone by milestone. Feature claims below are limited to what is
> implemented and tested today; the full product scope is described in the
> [documentation](docs/).

Currently implemented and verified:

- ✅ Monorepo foundation (Next.js dashboard, Go control plane, Python
  ingestion engine, Python SDK)
- ✅ Go control plane with health/readiness/version endpoints, structured
  logging and graceful shutdown
- ✅ Python ingestion engine service skeleton with typed configuration
- ✅ Python SDK client core with error taxonomy
- ✅ Repository tooling: Makefile, lint/test/build scripts, CI-ready structure

## Architecture (Target)

```text
               USERS / DATA TEAMS
                       |
                       v
               NEXT.JS DASHBOARD
                       |
                       v
                GO CONTROL PLANE
                       |
       +---------------+----------------+
       |               |                |
   AUTH/ORG        DATA CATALOG     GOVERNANCE
       |               |                |
       +---------------+----------------+
                       |
                  POSTGRESQL
                       |
               INGESTION SCHEDULER
                       |
                  NATS JETSTREAM
                       |
              PYTHON WORKER POOL
                       |
       +---------------+----------------+
       |               |                |
   CONNECTORS       PROFILER       QUALITY ENGINE
       |               |                |
       +---------------+----------------+
                       |
             EXTERNAL DATA SYSTEMS
```

DataAtlas follows a **modular monolith** architecture for the control plane
with independently running Python ingestion and quality workers, communicating
over PostgreSQL and NATS JetStream. See
[docs/architecture/](docs/architecture/) for the full design.

## Repository Structure

```text
DataAtlas/
├── apps/web/                 Next.js dashboard (App Router, TypeScript, Tailwind)
├── services/
│   ├── control-plane/        Go API / control plane (chi, pgx, NATS)
│   ├── ingestion-engine/     Python ingestion, profiling & quality engine (FastAPI)
│   └── sdk-python/           Official Python SDK
├── infra/                    Docker Compose and service configuration
├── docs/                     Architecture, database, API and security documentation
├── examples/                 Sample data, dbt project, OpenLineage fixtures
├── scripts/                  Development automation scripts
└── .github/workflows/        CI/CD pipelines
```

## Technology Stack

| Layer | Technology |
|---|---|
| Frontend | Next.js (App Router), React, TypeScript, Tailwind CSS, shadcn/ui, TanStack Query, React Flow, Recharts |
| Control plane | Go, chi, pgx, PostgreSQL |
| Ingestion & processing | Python, FastAPI, Pydantic, SQLAlchemy, DuckDB, PyArrow, SQLGlot |
| Metadata standards | OpenLineage, dbt artifacts |
| Infrastructure | Docker Compose, PostgreSQL, Redis, NATS JetStream, MinIO, OpenTelemetry, Prometheus, Grafana |

## Getting Started

### Prerequisites

- Node.js >= 22 and pnpm >= 10
- Go >= 1.25
- Python >= 3.13 with [uv](https://docs.astral.sh/uv/)
- Docker and Docker Compose

### Install and verify

```bash
# Install frontend dependencies
pnpm install

# Sync Python environments
cd services/ingestion-engine && uv sync && cd ../sdk-python && uv sync && cd ../..

# Run all linters and test suites
make lint
make test

# Build all components
make build
```

### Run the services in development mode

```bash
make dev   # Next.js (:3000) + Go control plane (:8080) + ingestion engine (:8090)
```

Health endpoints:

- Dashboard: http://localhost:3000
- Control plane: http://localhost:8080/healthz
- Ingestion engine: http://localhost:8090/healthz

## Documentation

- [Architecture](docs/architecture/) — system overview, planes, metadata model
- [Database](docs/database/) — ER diagrams and schema documentation
- [API](docs/api/) — REST API reference
- [Connectors](docs/connectors/) — connector framework and integrations
- [Security](docs/security/) — threat model and security architecture
- [Development](docs/development/) — setup, testing and troubleshooting guides

## Security

DataAtlas is designed security-first: source credentials are encrypted at
rest, external data sources are accessed strictly read-only, and tenant
isolation is enforced at every layer. See [SECURITY.md](SECURITY.md) and
[docs/security/threat-model.md](docs/security/threat-model.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, engineering
standards and pull request guidelines.

## License

[MIT](LICENSE)
