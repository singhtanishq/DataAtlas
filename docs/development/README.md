# Development Guide

## Prerequisites

- Node.js ≥ 22, pnpm ≥ 10
- Go ≥ 1.25
- Python ≥ 3.13 with [uv](https://docs.astral.sh/uv/)
- Docker with Compose

## Daily workflow

```bash
make infra-up        # PostgreSQL, Redis, NATS, object storage
make migrate         # apply SQL migrations (control plane)
make dev             # web :3000 + control plane :8080 + ingestion :8090
make lint            # gofmt/vet + ruff + mypy + eslint + tsc
make test            # Go unit + Python (integration tests skip w/o infra)
```

## Environment

Copy `.env.example` to `.env` and fill values. Generated secrets:

```bash
openssl rand -hex 32   # APP_ENCRYPTION_KEY / SESSION_SECRET
```

The application database role is `dataatlas_app` (NOSUPERUSER, NOBYPASSRLS)
so Row-Level Security always applies; migrations run as the table owner.

## Integration tests

Integration tests require the Docker stack and are tagged:

```bash
# Go (needs TEST_DATABASE_URL)
TEST_DATABASE_URL=... go test -tags=integration ./tests/

# Python (needs APP_ENCRYPTION_KEY + PGPASSWORD; skips when absent)
uv run pytest -q
```

## Migrations

```bash
make migrate            # apply pending
make migrate-status     # list applied/pending
bash scripts/migrate.sh down 1   # revert last
```

Migrations are embedded SQL files (`NNN_name.up.sql` / `.down.sql`) applied
transactionally under an advisory lock.

## Troubleshooting

- **Connection refused on :5432/:4222** — infrastructure down; run
  `make infra-up` (or `scripts/docker-watchdog.sh` to keep it alive).
- **`auth_unavailable` / 503** — control plane started without
  `DATABASE_URL`/`APP_ENCRYPTION_KEY`; export them from `.env`.
- **Cross-tenant empty results** — expected: queries without the tenant
  GUC fail closed. Service code must use `db.WithOrg`.
- **Worker not consuming** — check `uv run python -m app.workers.worker`
  logs; JetStream stream `INGESTION` is created by the control plane on
  first publish.

## Limitations

- Profiling and quality execute against sampled data by default.
- Column-level lineage only where authoritative events establish it.
- The local stack uses development-only credentials (documented); replace
  them for any shared deployment.
