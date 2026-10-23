#!/usr/bin/env bash
#
# Re-embed the directive registry into install.sh.
#
# install.sh is served standalone (WorkAiand serves it for `curl | sh`), so it
# cannot source install/registry.sh or read install/directives.tsv at runtime.
# Both are embedded in it verbatim. Run this after changing either canonical
# file; install/tests/registry_test.sh fails when the copies drift.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
install_dir="$script_dir/.."
installer="$install_dir/install.sh"
uninstaller="$install_dir/uninstall.sh"
registry="$install_dir/registry.sh"
data="$install_dir/directives.tsv"

for f in "$installer" "$uninstaller" "$registry" "$data"; do
  [ -f "$f" ] || { echo "missing $f" >&2; exit 1; }
done

lib="$(awk '/^# >>> aiand-router registry lib >>>$/{f=1;next} /^# <<< aiand-router registry lib <<<$/{f=0} f' "$registry")"
[ -n "$lib" ] || { echo "could not extract the registry lib region from $registry" >&2; exit 1; }

tmp="$(mktemp -t aiand-embed.XXXXXX)"
trap 'rm -f "$tmp"' EXIT

# Replace the data heredoc and the lib region in place, leaving everything else
# in the target script untouched.
embed_into() {
  local target="$1"
  LIB="$lib" DATA_FILE="$data" awk '
    /^AIAND_REGISTRY_DATA=\$\(cat <<.AIAND_REGISTRY_EOF.$/ {
      print
      while ((getline line < ENVIRON["DATA_FILE"]) > 0) print line
      close(ENVIRON["DATA_FILE"])
      skip_data = 1
      next
    }
    skip_data && /^AIAND_REGISTRY_EOF$/ { skip_data = 0; print; next }
    skip_data { next }
    /^# >>> aiand-router registry lib >>>$/ {
      print
      print ENVIRON["LIB"]
      skip_lib = 1
      next
    }
    skip_lib && /^# <<< aiand-router registry lib <<<$/ { skip_lib = 0; print; next }
    skip_lib { next }
    { print }
  ' "$target" >"$tmp"

  bash -n "$tmp" || { echo "re-embedded $target does not parse; leaving the original alone" >&2; exit 1; }

  if cmp -s "$tmp" "$target"; then
    echo "$(basename "$target") already up to date."
  else
    cat "$tmp" >"$target"
    echo "Re-embedded the registry into $(basename "$target")."
  fi
}

embed_into "$installer"
embed_into "$uninstaller"
