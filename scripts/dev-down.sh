#!/usr/bin/env bash
set -u
cd "$(dirname "$0")/.."
for name in backend frontend; do
  if [ -f ".local/$name.pid" ]; then
    pid="$(cat ".local/$name.pid")"
    kill -- "-$pid" 2>/dev/null || kill "$pid" 2>/dev/null || true
    rm -f ".local/$name.pid"
    echo "stopped $name"
  fi
done
# sweep stragglers
pkill -f CompileDaemon 2>/dev/null || true
pkill -f "next dev" 2>/dev/null || true
echo "dev servers down (Postgres left running; 'docker compose down' to stop db)"
