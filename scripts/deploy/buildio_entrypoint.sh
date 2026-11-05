#!/usr/bin/env bash
#
# build.io release entrypoint for the router (buildio-release stage).
#
# Responsibilities, in order:
#   1. Pub/Sub: the router panics at boot when Pub/Sub is unreachable
#      (main.go uses config.MustGet / fails fast on the client), so on
#      build.io — where no compose-style sidecar exists — we boot the
#      embedded Pub/Sub emulator in-process, wait for it to accept
#      connections, and pre-create the invalidation topic. Skipped
#      entirely when PUBSUB_EMULATOR_HOST is already set (the
#      docker-compose case, which runs the emulator sidecar).
#   2. Migrations: dockerfile-based PaaS stacks have no release phase,
#      so schema migrations run here at boot before the server starts.
#      Uses the golang-migrate binary plus psql to pre-create the
#      `router` schema (the migrate bookkeeping table needs it to
#      exist before `migrate up` runs with search_path=router).
#   3. exec the server binary so signals (SIGTERM) flow straight to
#      the Go process for graceful shutdown.
#
# Environment:
#   PUBSUB_EMULATOR_HOST              set => skip emulator boot
#   PUBSUB_EMULATOR_PORT              emulator port (default 8085)
#   PUBSUB_PROJECT_ID                 GCP project id (default router-build)
#   PUBSUB_TOPIC_ROUTER_INVALIDATION  topic to pre-create (main.go requires it)
#   ROUTER_RUN_MIGRATIONS             0 disables migrations (default: run)
#   DATABASE_URL                      required when migrations are enabled
set -euo pipefail

log() {
  printf '[entrypoint] %s\n' "$*" >&2
}

# --- 1. Embedded Pub/Sub emulator (only when nothing provides one) ---
if [ "${PUBSUB_EMULATOR_HOST:-}" = "" ]; then
  port="${PUBSUB_EMULATOR_PORT:-8085}"
  project="${PUBSUB_PROJECT_ID:-router-build}"
  topic="${PUBSUB_TOPIC_ROUTER_INVALIDATION:-}"
  log "PUBSUB_EMULATOR_HOST unset; booting embedded Pub/Sub emulator on 127.0.0.1:${port} (project ${project})"

  # /var/log may not exist on some minimal bases; keep the emulator's
  # output around for the failure path below.
  mkdir -p /var/log
  gcloud beta emulators pubsub start \
    --host-port="127.0.0.1:${port}" \
    --project="${project}" \
    &> /var/log/pubsub-emulator.log &

  ready=0
  for _ in $(seq 1 60); do
    if curl -sf "http://127.0.0.1:${port}" > /dev/null; then
      ready=1
      break
    fi
    sleep 1
  done
  if [ "$ready" != "1" ]; then
    log "ERROR: Pub/Sub emulator did not become ready within 60s; emulator log follows"
    cat /var/log/pubsub-emulator.log || true
    exit 1
  fi
  log "Pub/Sub emulator is ready on 127.0.0.1:${port}"

  export PUBSUB_EMULATOR_HOST="127.0.0.1:${port}"

  # Pre-create the invalidation topic. The gcppubsub client does not
  # create topics; main.go panics if the topic is missing. Retry a few
  # times in case the emulator's REST front-end settles late.
  if [ -n "$topic" ]; then
    topic_url="http://127.0.0.1:${port}/v1/projects/${project}/topics/${topic}"
    created=0
    for _ in $(seq 1 5); do
      if curl -sf -X PUT "$topic_url" > /dev/null; then
        created=1
        break
      fi
      sleep 1
    done
    if [ "$created" != "1" ]; then
      log "ERROR: could not create Pub/Sub topic ${topic} on the emulator"
      exit 1
    fi
    log "Pub/Sub topic ${topic} is ready"
  else
    log "WARNING: PUBSUB_TOPIC_ROUTER_INVALIDATION unset; skipping topic pre-create"
  fi
else
  log "PUBSUB_EMULATOR_HOST=${PUBSUB_EMULATOR_HOST} preset; skipping embedded emulator"
fi

# --- 2. Boot-time schema migrations (no release phase on dockerfile stacks) ---
if [ "${ROUTER_RUN_MIGRATIONS:-1}" != "0" ]; then
  if [ "${DATABASE_URL:-}" = "" ]; then
    log "ERROR: ROUTER_RUN_MIGRATIONS is enabled but DATABASE_URL is not set"
    exit 1
  fi

  log "Pre-creating schema router (migrate's bookkeeping table needs it)"
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -c 'CREATE SCHEMA IF NOT EXISTS router;'

  # golang-migrate takes no search_path flag; append it to the DSN.
  # DATABASE_URL may or may not already carry a query string
  # (e.g. postgres://…?sslmode=require).
  case "$DATABASE_URL" in
    *\?*) dsn="${DATABASE_URL}&search_path=router" ;;
    *)    dsn="${DATABASE_URL}?search_path=router" ;;
  esac

  log "Running migrations from /migrations"
  migrate -path /migrations -database "$dsn" up
  log "Migrations applied"
else
  log "ROUTER_RUN_MIGRATIONS=0; skipping migrations"
fi

# --- 3. Hand off to the server; exec keeps SIGTERM handling intact ---
exec /server
