#!/usr/bin/env bash
# Run all test suites across the DataAtlas monorepo.
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

run_step "Go control plane tests" bash -c "cd '${ROOT}/services/control-plane' && go test ./... && go vet ./..."
run_step "Python ingestion engine tests" bash -c "cd '${ROOT}/services/ingestion-engine' && uv run pytest -q"
run_step "Python SDK tests" bash -c "cd '${ROOT}/services/sdk-python' && uv run pytest -q"
run_step "Frontend typecheck" bash -c "cd '${ROOT}/apps/web' && pnpm typecheck"

if [[ "${FAILED}" -ne 0 ]]; then
  echo "One or more test suites FAILED."
  exit 1
fi
echo "All test suites passed."
