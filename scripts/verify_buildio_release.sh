#!/usr/bin/env bash
#
# Verify that build.io built and released a given commit, and that the
# production deployment is healthy.
#
# Checks, in order:
#   1. build.io created a `build-production` deployment for the SHA and its
#      latest status is `success` (the `Test` check being green is NOT proof
#      that the build succeeded).
#   2. Optionally, when BUILDIO_TOKEN is set, cross-check the build.io API for
#      the build matching the SHA and diagnose platform-side queue stalls.
#   3. build.io created a `production` deployment for the SHA with status
#      `success` (the release phase).
#   4. The production URL answers 200 on /livez, /readyz and /startupz.
#
# Environment:
#   GH_TOKEN       token for `gh api` (or a locally logged-in `gh`)
#   BUILDIO_TOKEN  optional build.io API token for build-level diagnostics
#   BUILDIO_APP    optional override of the build.io app name (default below)
#
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: scripts/verify_buildio_release.sh (--sha SHA | --latest) \
  [--repo OWNER/REPO] [--app-url URL] \
  [--deployment-timeout-seconds N] [--status-timeout-seconds N] \
  [--release-timeout-seconds N] [--health-retries N] \
  [--health-interval-seconds N] [--buildio-app APP] [--since-epoch SECONDS]

Modes (exactly one required):
  --sha SHA        Verify the release of a specific commit: wait for its
                   build-production deployment, its success, then a
                   successful production deployment for the same SHA.
  --latest         Verify the latest production deployment created at or
                   after --since-epoch (default 0): build.io may release the
                   generated `deploy` branch (image-pin commit) rather than
                   the pushed SHA. Requires only a successful production
                   deployment; the build-production deployment is reported
                   in diagnostics when present but is not required.
Options:
  --since-epoch S  With --latest: only production deployments created at or
                   after this epoch second count (default 0).
EOF
  exit 2
}

sha=""
latest=false
since=0
repo="${GITHUB_REPOSITORY:-fenilmodi00/aiand_router}"
app_url="https://aiand-router-f0eb1883.onbld.com"
deployment_timeout=600
status_timeout=1800
release_timeout=600
health_retries=12
health_interval=5
buildio_app="${BUILDIO_APP:-aiand-router}"

while [ $# -gt 0 ]; do
  case "$1" in
    --sha) sha="${2:-}"; shift 2 ;;
    --latest) latest=true; shift ;;
    --since-epoch) since="${2:-}"; shift 2 ;;
    --repo) repo="${2:-}"; shift 2 ;;
    --app-url) app_url="${2:-}"; shift 2 ;;
    --deployment-timeout-seconds) deployment_timeout="${2:-}"; shift 2 ;;
    --status-timeout-seconds) status_timeout="${2:-}"; shift 2 ;;
    --release-timeout-seconds) release_timeout="${2:-}"; shift 2 ;;
    --health-retries) health_retries="${2:-}"; shift 2 ;;
    --health-interval-seconds) health_interval="${2:-}"; shift 2 ;;
    --buildio-app) buildio_app="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; usage ;;
  esac
done

if { [ -n "$sha" ] && [ "$latest" = true ]; } || { [ -z "$sha" ] && [ "$latest" = false ]; }; then
  printf '::error::exactly one of --sha SHA or --latest is required\n'
  usage
fi
if [ "$latest" = true ]; then
  case "$since" in
    ''|*[!0-9]*) printf '::error::--since-epoch must be a non-negative integer, got: %s\n' "$since"; exit 1 ;;
  esac
else
  if ! [[ "$sha" =~ ^[0-9a-fA-F]{40}$ ]]; then
    printf '::error::SHA must be 40 hex characters, got: %s\n' "$sha"
    exit 1
  fi
  sha=$(printf '%s' "$sha" | tr '[:upper:]' '[:lower:]')
fi

for tool in gh jq curl; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    printf '::error::required tool not found: %s\n' "$tool"
    exit 1
  fi
done
if [ -z "${GH_TOKEN:-}" ]; then
  if ! gh auth status >/dev/null 2>&1; then
    printf '::error::GH_TOKEN is not set and gh is not logged in; cannot call the GitHub API\n'
    exit 1
  fi
fi

# gh api prints "message: ..." on failures; keep quiet and handle errors here.
gh_api() {
  gh api "$1" --jq "$2" 2>/dev/null
}

log() { printf '[verify-release] %s\n' "$*" >&2; }

# ISO-8601 (e.g. 2026-11-13T09:58:13Z) -> epoch seconds. GNU date is the
# runner default; fall back to jq with fractional seconds stripped.
iso_to_epoch() {
  local ts="$1" out
  out=$(date -d "$ts" +%s 2>/dev/null) && { printf '%s' "$out"; return 0; }
  out=$(printf '%s' "$ts" | sed 's/\.[0-9]*//' | jq -r 'fromdateiso8601' 2>/dev/null) || return 1
  printf '%s' "$out"
}

# ---------------------------------------------------------------------------
# --latest mode: verify the newest production deployment created since the
# run started (build.io thin-deploys the generated `deploy` branch commit,
# so the pushed SHA never gets its own deployment in that model).
# ---------------------------------------------------------------------------
if [ "$latest" = true ]; then
  since_iso=$(date -u -d "@$since" '+%Y-%m-%dT%H:%M:%SZ')
  log "mode: latest (production deployments created at/after $since_iso)"

  prod_deployment_id=""
  prod_created_at=""
  deadline=$(( $(date +%s) + deployment_timeout ))
  while :; do
    line=$(gh_api "repos/$repo/deployments?per_page=100" \
      '[.[] | select(.environment == "production")] | sort_by(.created_at) | last | (.id|tostring) + " " + .created_at // empty') || line=""
    if [ -n "$line" ]; then
      prod_deployment_id="${line%% *}"
      prod_created_at="${line#* }"
      prod_epoch=$(iso_to_epoch "$prod_created_at") || prod_epoch=0
      if [ "$prod_epoch" -ge "$since" ]; then
        log "found production deployment $prod_deployment_id created $prod_created_at"
        break
      fi
      prod_deployment_id=""; prod_created_at=""
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      printf '::error::no new production deployment for %s since %s; build.io did not release the commit. Check the build queue with: bld builds -a %s\n' "$repo" "$since_iso" "$buildio_app"
      exit 1
    fi
    sleep 5
  done

  prod_status=""
  deadline=$(( $(date +%s) + status_timeout ))
  while :; do
    prod_status=$(gh_api "repos/$repo/deployments/$prod_deployment_id/statuses?per_page=100" \
      'sort_by(.created_at) | last | .state // empty') || prod_status=""
    case "$prod_status" in
      success|failure|error|inactive)
        log "production deployment $prod_deployment_id status: $prod_status"
        break
        ;;
    esac
    if [ "$(date +%s)" -ge "$deadline" ]; then
      printf '::error::timed out after %ss waiting for a terminal status on production deployment %s; last status: %s\n' "$status_timeout" "$prod_deployment_id" "${prod_status:-none}"
      exit 1
    fi
    sleep 5
  done

  case "$prod_status" in
    success) ;;
    failure|error|inactive)
      printf '::error::release failed: production deployment %s has status %s. Check the build.io release logs with: bld logs -a %s -s release -c 2000\n' "$prod_deployment_id" "$prod_status" "$buildio_app"
      exit 1
      ;;
    *)
      printf '::error::production deployment %s has unexpected status %s\n' "$prod_deployment_id" "$prod_status"
      exit 1
      ;;
  esac

  # Diagnostics only: report the build-production deployment for the same
  # commit when one exists. Thin deploys may not have one; never fail here.
  prod_commit=$(gh_api "repos/$repo/deployments/$prod_deployment_id" '.sha // empty') || prod_commit=""
  if [ -n "$prod_commit" ]; then
    build_dep_id=$(gh_api "repos/$repo/deployments?sha=$prod_commit&per_page=100" \
      '[.[] | select(.environment == "build-production")] | sort_by(.created_at) | last | .id // empty') || build_dep_id=""
    if [ -n "$build_dep_id" ]; then
      build_dep_status=$(gh_api "repos/$repo/deployments/$build_dep_id/statuses?per_page=100" \
        'sort_by(.created_at) | last | .state // empty') || build_dep_status=""
      build_id=$(gh_api "repos/$repo/deployments/$build_dep_id" '.payload.build_id // empty') || build_id=""
      log "build-production deployment $build_dep_id for $prod_commit: status ${build_dep_status:-none}${build_id:+, build_id $build_id}"
    else
      log "no build-production deployment for $prod_commit (thin deploy: build ran for the generated deploy-branch commit)"
    fi
  fi

  status="$prod_status"
else
# ---------------------------------------------------------------------------
# --sha mode: wait for build.io to create the `build-production` deployment.
# ---------------------------------------------------------------------------
deadline=$(( $(date +%s) + deployment_timeout ))
while :; do
  deployment_id=$(gh_api "repos/$repo/deployments?sha=$sha&per_page=100" \
    '[.[] | select(.environment == "build-production")] | sort_by(.created_at) | last | .id // empty') || deployment_id=""
  if [ -n "$deployment_id" ]; then
    log "found build-production deployment $deployment_id for $sha"
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    printf '::error::no build-production deployment was created for %s within %ss. build.io likely never accepted or started the build. Check the build.io Deploy tab or run: bld builds -a %s\n' "$sha" "$deployment_timeout" "$buildio_app"
    exit 1
  fi
  sleep 5
done

# ---------------------------------------------------------------------------
# Optional: build.io API cross-check / queue-stall diagnosis.
# ---------------------------------------------------------------------------
if [ -n "${BUILDIO_TOKEN:-}" ]; then
  buildio_state=$(curl -fsS -m 30 \
    -H "Authorization: Bearer $BUILDIO_TOKEN" \
    -H "Accept: application/json" \
    "https://app.build.io/api/v1/apps/$buildio_app/builds?limit=100" 2>/dev/null \
    | jq -r --arg sha "$sha" \
      '[.[] | select((.source_blob.version // "") == $sha)][0] | (.state // "unknown") + "|" + (.id // "") + "|" + (.created_at // "")' \
    || true)
  if [ -n "$buildio_state" ]; then
    state="${buildio_state%%|*}"
    rest="${buildio_state#*|}"
    bid="${rest%%|*}"
    created_at="${rest#*|}"
    log "build.io API: build id=$bid state=$state created_at=$created_at"
    if [ "$state" = "pending" ]; then
      pending_since=$(date +%s)
      while :; do
        sleep 15
        state=$(curl -fsS -m 30 \
          -H "Authorization: Bearer $BUILDIO_TOKEN" \
          -H "Accept: application/json" \
          "https://app.build.io/api/v1/apps/$buildio_app/builds?limit=100" 2>/dev/null \
          | jq -r --arg sha "$sha" \
            '[.[] | select((.source_blob.version // "") == $sha)][0].state // empty' \
          || true)
        [ "$state" != "pending" ] && break
        if [ $(( $(date +%s) - pending_since )) -gt 300 ]; then
          printf '::error::build.io build %s for %s has been pending for more than 300s: build.io accepted the build but its builder has not started it. This is a platform-side build queue failure, not a repo failure. Check https://build.betteruptime.com/, retry with: git push buildio %s:refs/heads/main, and contact support@build.io / emergency@build.io\n' "$bid" "$sha" "$sha"
          exit 1
        fi
      done
      log "build.io build $bid left pending (state=$state)"
    fi
  else
    log "build.io API returned no build for $sha (continuing with GitHub deployments)"
  fi
fi

# ---------------------------------------------------------------------------
# 2. Wait for the build-production deployment status to be `success`.
# ---------------------------------------------------------------------------
status=""
deadline=$(( $(date +%s) + status_timeout ))
while :; do
  status=$(gh_api "repos/$repo/deployments/$deployment_id/statuses?per_page=100" \
    'sort_by(.created_at) | last | .state // empty') || status=""
  case "$status" in
    success|failure|error|inactive)
      log "build-production deployment $deployment_id status: $status"
      break
      ;;
  esac
  if [ "$(date +%s)" -ge "$deadline" ]; then
    printf '::error::timed out after %ss waiting for a terminal status on build-production deployment %s (sha %s); last status: %s\n' "$status_timeout" "$deployment_id" "$sha" "${status:-none}"
    exit 1
  fi
  sleep 5
done

case "$status" in
  success) ;;
  failure|error|inactive)
    build_id=$(gh_api "repos/$repo/deployments/$deployment_id" '.payload.build_id // empty' || true)
    printf '::error::build failed: build-production deployment %s (sha %s) has status %s%s. Fetch build logs with: bld logs -a %s -s build -c 2000\n' \
      "$deployment_id" "$sha" "$status" \
      "${build_id:+, build_id $build_id}" "$buildio_app"
    exit 1
    ;;
  *)
    printf '::error::build-production deployment %s (sha %s) has unexpected status %s\n' "$deployment_id" "$sha" "$status"
    exit 1
    ;;
esac

# ---------------------------------------------------------------------------
# 3. Wait for the `production` (release) deployment to succeed. It may already
#    exist by the time we look.
# ---------------------------------------------------------------------------
prod_deployment_id=""
prod_status=""
deadline=$(( $(date +%s) + release_timeout ))
while :; do
  prod_deployment_id=$(gh_api "repos/$repo/deployments?sha=$sha&per_page=100" \
    '[.[] | select(.environment == "production")] | sort_by(.created_at) | last | .id // empty') || prod_deployment_id=""
  if [ -n "$prod_deployment_id" ]; then
    prod_status=$(gh_api "repos/$repo/deployments/$prod_deployment_id/statuses?per_page=100" \
      'sort_by(.created_at) | last | .state // empty') || prod_status=""
    if [ "$prod_status" = "success" ]; then
      log "production deployment $prod_deployment_id status: success"
      break
    fi
    if [ "$prod_status" = "failure" ] || [ "$prod_status" = "error" ] || [ "$prod_status" = "inactive" ]; then
      printf '::error::release failed: production deployment %s (sha %s) has status %s. Check the build.io release logs for this build.\n' "$prod_deployment_id" "$sha" "$prod_status"
      exit 1
    fi
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    printf '::error::timed out after %ss waiting for a successful production deployment for %s%s\n' \
      "$release_timeout" "$sha" \
      "${prod_deployment_id:+ (latest production deployment $prod_deployment_id, status ${prod_status:-none})}"
    exit 1
  fi
  sleep 5
done

fi # latest / sha mode

# ---------------------------------------------------------------------------
# 4. Health checks against the production URL.
# ---------------------------------------------------------------------------
for path in /livez /readyz /startupz; do
  attempt=1
  while :; do
    code=$(curl -sS -o /dev/null -m 15 -w '%{http_code}' "$app_url$path" 2>/dev/null || true)
    code=${code:-000}
    if [ "$code" = "200" ]; then
      log "$path -> 200"
      break
    fi
    if [ "$attempt" -ge "$health_retries" ]; then
      printf '::error::health check failed: %s%s returned HTTP %s after %s attempts\n' "$app_url" "$path" "$code" "$attempt"
      exit 1
    fi
    log "$path -> $code (attempt $attempt/$health_retries); retrying in ${health_interval}s"
    attempt=$(( attempt + 1 ))
    sleep "$health_interval"
  done
done

# Informational only; build.io does not pass ROUTER_SHA, so `commit: unknown`
# here is normal and must not fail the verification.
version_body=$(curl -sS -m 15 "$app_url/v1/version" 2>/dev/null || true)
log "/v1/version: $version_body"

if [ "$latest" = true ]; then
  printf '[verify-release] SUCCESS: latest production deployment %s (created %s) status %s; %s healthy (livez/readyz/startupz all 200)\n' \
    "$prod_deployment_id" "$prod_created_at" "$prod_status" "$app_url"
else
  printf '[verify-release] SUCCESS: sha %s — build-production deployment %s status %s; production deployment %s status %s; %s healthy (livez/readyz/startupz all 200)\n' \
    "$sha" "$deployment_id" "$status" "$prod_deployment_id" "$prod_status" "$app_url"
fi
exit 0
