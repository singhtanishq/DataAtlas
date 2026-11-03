#!/usr/bin/env bash
# Run all linters and static analysis across the monorepo.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FAILED=0

run_step() {
  local name="$1"; shift
  echo "==> ${name}"
  if ! "$@"; then
    echo "!! FAILED: ${name}"
    FAILED=1
  fi
}

run_step "Go formatting check" bash -c "cd '${ROOT}/services/control-plane' && test -z \"\$(gofmt -l .)\" || (gofmt -l . && exit 1)"
run_step "Go vet" bash -c "cd '${ROOT}/services/control-plane' && go vet ./..."
run_step "Python ingestion engine lint" bash -c "cd '${ROOT}/services/ingestion-engine' && uv run ruff check . && uv run mypy app"
run_step "Python SDK lint" bash -c "cd '${ROOT}/services/sdk-python' && uv run ruff check . && uv run mypy dataatlas"
run_step "Frontend lint" bash -c "cd '${ROOT}/apps/web' && pnpm lint"

if [[ "${FAILED}" -ne 0 ]]; then
  echo "One or more linters FAILED."
  exit 1
fi
echo "All linters passed."
