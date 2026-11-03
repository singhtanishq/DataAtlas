#!/usr/bin/env bash
# Build all application components of the DataAtlas monorepo.
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

run_step "Go control plane build" bash -c "cd '${ROOT}/services/control-plane' && go build -o bin/control-plane ./cmd/server"
run_step "Frontend build" bash -c "cd '${ROOT}/apps/web' && pnpm build"

if [[ "${FAILED}" -ne 0 ]]; then
  echo "One or more builds FAILED."
  exit 1
fi
echo "All builds succeeded."
