#!/usr/bin/env bash
# Apply DataAtlas control-plane database migrations.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ENV_FILE="${ROOT}/.env"
if [[ -f "${ENV_FILE}" ]]; then
  POSTGRES_USER=$(sed -n 's/^POSTGRES_USER=//p' "${ENV_FILE}")
  POSTGRES_PASSWORD=$(sed -n 's/^POSTGRES_PASSWORD=//p' "${ENV_FILE}")
  POSTGRES_DB=$(sed -n 's/^POSTGRES_DB=//p' "${ENV_FILE}")
fi
POSTGRES_USER="${POSTGRES_USER:-dataatlas}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-dataatlas}"
POSTGRES_DB="${POSTGRES_DB:-dataatlas}"

export DATABASE_URL="${DATABASE_URL:-postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@localhost:5432/${POSTGRES_DB}?sslmode=disable}"

cd "${ROOT}/services/control-plane"
command="${1:-up}"
shift 2>/dev/null || true
exec go run ./cmd/migrate "${command}" "$@"
