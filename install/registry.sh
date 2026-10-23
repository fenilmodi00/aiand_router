#!/usr/bin/env bash
#
# Shared, shell-portable directive registry helpers. Columns are documented in
# directives.tsv, which is the canonical declaration.
#
# install.sh is served standalone (WorkAiand serves it for `curl | sh`, and it
# has no sibling files there), so it carries an embedded copy of both this
# helper block and the registry data. The region between the markers below is
# copied verbatim into install.sh; install/tests/registry_test.sh asserts the
# two never drift. Callers that DO have the files alongside them (uninstall.sh,
# the packaged installer) source this file instead.
#
# Rows come from $AIAND_REGISTRY_DATA when set (install.sh's embedded copy),
# otherwise from directives.tsv next to this script.

# >>> aiand-router registry lib >>>
aiand_registry_rows() {
  if [ -n "${AIAND_REGISTRY_DATA:-}" ]; then
    printf '%s\n' "$AIAND_REGISTRY_DATA" | awk -F '|' '!/^([[:space:]]*#|[[:space:]]*$)/ { print }'
    return 0
  fi
  local registry="${AIAND_REGISTRY_FILE:-}"
  if [ -z "$registry" ]; then
    local dir
    dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd -P)" || return 1
    registry="$dir/directives.tsv"
  fi
  [ -f "$registry" ] || return 1
  awk -F '|' '!/^([[:space:]]*#|[[:space:]]*$)/ { print }' "$registry"
}

# aiand_registry_names lists every canonical name and alias a client installs.
aiand_registry_names() {
  local target="$1" canonical aliases capability claude codex opencode pi adapter
  while IFS='|' read -r canonical aliases capability claude codex opencode pi adapter; do
    case "$target" in
      claude) [ "$claude" = yes ] || continue ;; codex) [ "$codex" = yes ] || continue ;;
      opencode) [ "$opencode" = yes ] || continue ;; pi) [ "$pi" = yes ] || continue ;;
    esac
    printf '%s\n' "$canonical"
    [ -n "$aliases" ] && printf '%s\n' "$aliases" | tr ',' '\n'
  done <<EOF
$(aiand_registry_rows)
EOF
}

# aiand_registry_skill_names lists the prompt directives a client exposes as a
# native skill. Local-config toggles are excluded: they mutate config this
# installer owns and are handled per target, not as a generic prompt.
aiand_registry_skill_names() {
  local target="$1" canonical aliases capability claude codex opencode pi adapter
  while IFS='|' read -r canonical aliases capability claude codex opencode pi adapter; do
    [ "$capability" = prompt ] || continue
    case "$target" in
      claude) [ "$claude" = yes ] || continue ;; codex) [ "$codex" = yes ] || continue ;;
      opencode) [ "$opencode" = yes ] || continue ;; pi) [ "$pi" = yes ] || continue ;;
    esac
    printf '%s\n' "$canonical"
    [ -n "$aliases" ] && printf '%s\n' "$aliases" | tr ',' '\n'
  done <<EOF
$(aiand_registry_rows)
EOF
}

# aiand_registry_skill_assets lists every directive a client ships as a skill
# file, including local-config toggles such as Codex's disable-routing. Install
# and uninstall use this for file management; aiand_registry_skill_names is the
# narrower prompt-only set used when generating prompt adapters.
aiand_registry_skill_assets() {
  local target="$1" canonical aliases capability claude codex opencode pi adapter
  while IFS='|' read -r canonical aliases capability claude codex opencode pi adapter; do
    case ",$adapter," in *,skill,*) ;; *) continue ;; esac
    case "$target" in
      claude) [ "$claude" = yes ] || continue ;; codex) [ "$codex" = yes ] || continue ;;
      opencode) [ "$opencode" = yes ] || continue ;; pi) [ "$pi" = yes ] || continue ;;
    esac
    printf '%s\n' "$canonical"
    [ -n "$aliases" ] && printf '%s\n' "$aliases" | tr ',' '\n'
  done <<EOF
$(aiand_registry_rows)
EOF
}

# aiand_registry_canonical_for resolves a name or alias to its canonical
# directive, and fails when the name is not in the registry at all.
aiand_registry_canonical_for() {
  local wanted="$1" canonical aliases capability claude codex opencode pi adapter alias
  while IFS='|' read -r canonical aliases capability claude codex opencode pi adapter; do
    [ "$wanted" = "$canonical" ] && { printf '%s' "$canonical"; return 0; }
    IFS=',' read -ra _aliases <<< "$aliases"
    for alias in ${_aliases[@]+"${_aliases[@]}"}; do
      [ "$wanted" = "$alias" ] && { printf '%s' "$canonical"; return 0; }
    done
  done <<EOF
$(aiand_registry_rows)
EOF
  return 1
}
# <<< aiand-router registry lib <<<
