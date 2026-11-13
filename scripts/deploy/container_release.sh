#!/usr/bin/env bash
# Portable container release for aiand-router.
#
# Pulls a GHCR image, replaces the running container, waits for the health
# endpoint, and rolls back to the previous image if health checks fail.
# Host-agnostic: works on any VPS, boxd VM, or bare Docker host.
#
# Recommend digest-pinned images (ghcr.io/owner/repo@sha256:<digest>) so the
# rollback target and the deployed build are exact.
#
# Usage:
#   scripts/deploy/container_release.sh --image IMAGE --env-file FILE \
#     [--name aiand-router] [--port 8080] [--health-path /livez] \
#     [--health-retries 30] [--health-interval 2] [--dry-run]
set -euo pipefail

name="aiand-router"
port="8080"
bind="127.0.0.1"
health_path="/livez"
health_retries="30"
health_interval="2"
image=""
env_file=""
dry_run="false"

usage() {
  cat <<'EOF'
Usage: scripts/deploy/container_release.sh --image IMAGE --env-file FILE
                 [--name aiand-router] [--port 8080] [--bind 127.0.0.1]
                 [--health-path /livez] [--health-retries 30]
                 [--health-interval 2] [--dry-run]

Options:
  --image IMAGE          Container image to deploy. Recommend digest-pinned
                         references (ghcr.io/owner/repo@sha256:<digest>) so the
                         deployed build and rollback target are exact.
  --env-file FILE        Path to a docker --env-file (readable, non-empty).
                         Its contents are never printed by this script.
  --name NAME            Container name (default: aiand-router).
  --port PORT            Host and container port (default: 8080).
  --bind IP              Host IP/interface for the published port
                         (default: 127.0.0.1). Only a local reverse proxy can
                         reach the container unless you pass 0.0.0.0.
  --health-path PATH     Health endpoint path (default: /livez).
  --health-retries N     Health check attempts before rollback (default: 30).
  --health-interval S    Seconds between health checks (default: 2).
  --dry-run              Print the exact docker run command and exit 0
                         without pulling, stopping, or running anything.
  -h, --help             Show this help.
EOF
}

die() {
  echo "error: $*" >&2
  exit 1
}

while [ $# -gt 0 ]; do
  case "$1" in
    --image)
      [ $# -ge 2 ] || die "--image requires a value"
      image="$2"
      shift 2
      ;;
    --env-file)
      [ $# -ge 2 ] || die "--env-file requires a value"
      env_file="$2"
      shift 2
      ;;
    --name)
      [ $# -ge 2 ] || die "--name requires a value"
      name="$2"
      shift 2
      ;;
    --port)
      [ $# -ge 2 ] || die "--port requires a value"
      port="$2"
      shift 2
      ;;
    --bind)
      [ $# -ge 2 ] || die "--bind requires a value"
      bind="$2"
      shift 2
      ;;
    --health-path)
      [ $# -ge 2 ] || die "--health-path requires a value"
      health_path="$2"
      shift 2
      ;;
    --health-retries)
      [ $# -ge 2 ] || die "--health-retries requires a value"
      health_retries="$2"
      shift 2
      ;;
    --health-interval)
      [ $# -ge 2 ] || die "--health-interval requires a value"
      health_interval="$2"
      shift 2
      ;;
    --dry-run)
      dry_run="true"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      die "unknown argument: $1"
      ;;
  esac
done

command -v docker >/dev/null 2>&1 || die "docker is required but not installed or not on PATH"
command -v curl >/dev/null 2>&1 || die "curl is required but not installed or not on PATH"

[ -n "$image" ] || die "--image is required"
[ -n "$env_file" ] || die "--env-file is required"

# Image reference: non-empty, no whitespace. Digest-pinned references
# (@sha256:<hex>) are strongly recommended for reproducible releases.
case "$image" in
  *[[:space:]]*) die "image reference must not contain whitespace: '$image'" ;;
  *) ;;
esac

[ -r "$env_file" ] || die "env file '$env_file' is not readable"
[ -s "$env_file" ] || die "env file '$env_file' is empty"

# Bind address: non-empty IP/host, no whitespace.
[ -n "$bind" ] || die "--bind requires a value"
case "$bind" in
  *[[:space:]]*) die "bind address must not contain whitespace: '$bind'" ;;
  *) ;;
esac

run_container() {
  local image_ref="$1"
  docker run -d --name "$name" --restart=always --env-file "$env_file" -p "$bind:$port:$port" "$image_ref"
}

print_run_cmd() {
  local image_ref="$1"
  echo "docker run -d --name \"$name\" --restart=always --env-file \"$env_file\" -p \"$bind:$port:$port\" \"$image_ref\""
}

if [ "$dry_run" = "true" ]; then
  echo "dry-run: would run the following command (env file path shown, contents not read):"
  print_run_cmd "$image"
  exit 0
fi

# Record the currently deployed image ID for rollback.
previous_image_id=""
if docker container inspect "$name" >/dev/null 2>&1; then
  previous_image_id="$(docker inspect --format '{{.Image}}' "$name")"
  echo "recorded previous image ID for rollback: $previous_image_id"
fi

echo "pulling $image"
docker pull "$image"

echo "stopping and removing existing container '$name' (if present)"
if docker container inspect "$name" >/dev/null 2>&1; then
  # Graceful stop first: let the router's SIGTERM handler drain (30s grace,
  # ignores failure); docker rm -f is the fallback.
  docker stop --time 30 "$name" >/dev/null 2>&1 || true
fi
docker rm -f "$name" >/dev/null 2>&1 || true

echo "starting container"
print_run_cmd "$image"
run_container "$image"

health_url="http://127.0.0.1:$port$health_path"
attempt=1
while [ "$attempt" -le "$health_retries" ]; do
  if curl -fsS --max-time 5 "$health_url" >/dev/null 2>&1; then
    running_image_id="$(docker inspect --format '{{.Image}}' "$name")"
    repo_digest="$(docker inspect --format '{{index .RepoDigests 0}}' "$image" 2>/dev/null || echo "unknown")"
    echo "health check passed at $health_url"
    echo "running image ID: $running_image_id"
    echo "RepoDigest: $repo_digest"
    echo "health path: $health_path"
    exit 0
  fi
  echo "health check $attempt/$health_retries failed; retrying in ${health_interval}s"
  sleep "$health_interval"
  attempt=$((attempt + 1))
done

echo "health check failed after $health_retries attempts at $health_url" >&2
echo "--- last 100 log lines of the failed container ---" >&2
docker logs --tail 100 "$name" >&2 || true
echo "--- end log output ---" >&2
if docker container inspect "$name" >/dev/null 2>&1; then
  docker stop --time 30 "$name" >/dev/null 2>&1 || true
fi
docker rm -f "$name" >/dev/null 2>&1 || true

if [ -n "$previous_image_id" ]; then
  echo "rolling back to previous image $previous_image_id"
  if run_container "$previous_image_id"; then
    echo "rolled back to previous image $previous_image_id; release FAILED"
  else
    die "rollback failed; container '$name' is down, inspect '$previous_image_id' manually"
  fi
else
  echo "no previous image recorded; nothing to roll back" >&2
fi
exit 1
