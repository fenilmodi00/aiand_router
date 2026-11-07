#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .local

# Database + pubsub emulator
docker compose up -d postgres pubsub-emulator >/dev/null 2>&1 || true
for i in $(seq 1 30); do
  docker compose exec -T postgres pg_isready -U router -d router >/dev/null 2>&1 && break
  sleep 1
done
make migrate-up >/dev/null 2>&1 || true

# Backend (CompileDaemon hot-reload)
if [ -f .local/backend.pid ] && kill -0 "$(cat .local/backend.pid)" 2>/dev/null; then
  echo "backend already running (pid $(cat .local/backend.pid))"
else
  setsid bash -c 'PATH="$HOME/go/bin:$PATH" exec make dev' > .local/backend.log 2>&1 &
  echo $! > .local/backend.pid
  echo "backend started (pid $!) -> http://localhost:8080 (log: .local/backend.log)"
fi

# Frontend (Next.js HMR)
if [ -f .local/frontend.pid ] && kill -0 "$(cat .local/frontend.pid)" 2>/dev/null; then
  echo "frontend already running (pid $(cat .local/frontend.pid))"
else
  (cd frontend && setsid bash -c 'exec npm run dev' > ../.local/frontend.log 2>&1 &)
  # capture the npm process pid written above is the subshell's; re-find
  sleep 1
  pgrep -f "next dev" | head -1 > .local/frontend.pid 2>/dev/null || true
  echo "frontend started -> http://localhost:3000 (log: .local/frontend.log)"
fi
