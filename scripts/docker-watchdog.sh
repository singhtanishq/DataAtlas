#!/usr/bin/env bash
# Keeps the Docker daemon and the DataAtlas core stack reachable on hosts
# where the Docker VM is prone to dying or losing port forwarding under
# memory pressure. Probes real ports, not container state.
set -u
cd "$(dirname "${BASH_SOURCE[0]}")/.."

probe() { nc -z localhost "$1" &>/dev/null; }

ensure_daemon() {
  if ! docker info --format ok &>/dev/null; then
    echo "[watchdog $(date +%H:%M:%S)] daemon down; starting Docker Desktop"
    open -a Docker 2>/dev/null
    for _ in $(seq 1 60); do
      docker info --format ok &>/dev/null && return 0
      sleep 2
    done
  fi
  return 0
}

while true; do
  if ! probe 5432 || ! probe 4222 || ! probe 6379; then
    echo "[watchdog $(date +%H:%M:%S)] stack ports unreachable"
    ensure_daemon
    docker compose down 2>/dev/null | tail -1
    docker compose up -d 2>&1 | tail -1
    # Wait for ports to come up; escalate to a full daemon restart once.
    up_ok=0
    for _ in $(seq 1 24); do
      if probe 5432 && probe 4222 && probe 6379; then up_ok=1; break; fi
      sleep 5
    done
    if [ "$up_ok" -ne 1 ]; then
      echo "[watchdog $(date +%H:%M:%S)] escalating: full Docker restart"
      osascript -e 'quit app "Docker"' 2>/dev/null
      sleep 5
      ensure_daemon
      docker compose up -d 2>&1 | tail -1
    fi
  fi
  sleep 15
done
