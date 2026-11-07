#!/usr/bin/env bash
set -u
cd "$(dirname "$0")/.."

# kill any leftover listeners from a previous run (handles stale frontend)
pkill -f CompileDaemon 2>/dev/null || true
pkill -f "next dev" 2>/dev/null || true
pkill -f "bin/server" 2>/dev/null || true
sleep 1

COMPILEDAEMON="$HOME/go/bin/CompileDaemon"
FRONT_PID=""
cleanup() {
  if [ -n "$FRONT_PID" ] && kill -0 "$FRONT_PID" 2>/dev/null; then
    kill "$FRONT_PID" 2>/dev/null || true
    wait "$FRONT_PID" 2>/dev/null || true
  fi
  pkill -f "next dev" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# Frontend first
PORT=3000 npm --prefix frontend run dev > .local/frontend.log 2>&1 &
FRONT_PID=$!
sleep 2

# Backend hot-reload
PATH="$HOME/go/bin:$PATH" "$COMPILEDAEMON" \
  -build="go build -tags ORT -o ./bin/server ./cmd/router" \
  -command="./bin/server" \
  -exclude-dir="vendor" \
  -exclude-dir=".vscode" \
  -exclude-dir="bin" \
  -exclude-dir=".venv" \
  -exclude-dir="__pycache__" \
  -exclude-dir=".pytest_cache" \
  -exclude-dir=".mypy_cache" \
  -exclude-dir=".ruff_cache" \
  -exclude-dir=".bench-cache" \
  -exclude-dir=".embedding-cache" \
  -exclude-dir="node_modules" \
  -exclude-dir="results" \
  -exclude-dir="logs" \
  -exclude-dir="assets" \
  -exclude-dir=".git" \
  -exclude-dir="eval" \
  -exclude-dir="scripts" \
  -exclude-dir="docs" \
  -exclude-dir=".local" \
  -exclude-dir="install" \
  -pattern="(.+\.go|.+\.sql)$$" \
  -graceful-kill=true \
  -log-prefix=false
