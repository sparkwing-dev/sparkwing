#!/usr/bin/env bash
# Run shellcheck across every tracked shell script in the repo.
#
# Discovers scripts by file extension (`*.sh`) plus any tracked file
# whose first line is a bash/sh shebang. Skips vendor / node_modules
# / .git automatically because git ls-files already excludes those.
#
# Exit: 0 if clean, non-zero with shellcheck's own output if any
# script has findings. shellcheck does not need a config file for the
# default rule set; per-script suppressions live as `# shellcheck
# disable=SCxxxx` comments in the script itself.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if ! command -v shellcheck >/dev/null 2>&1; then
  echo "check-shell: shellcheck not installed (brew install shellcheck)" >&2
  exit 1
fi

# Tracked .sh files first.
mapfile -t scripts < <(git ls-files '*.sh' 2>/dev/null | sort -u)

# Plus any tracked file whose first line is a bash/sh shebang.
# Common for `bin/` scripts that don't carry the .sh suffix.
while IFS= read -r f; do
  [[ -z "$f" || ! -f "$f" ]] && continue
  case "$f" in
    *.sh) continue ;;
  esac
  head -c 64 "$f" 2>/dev/null | head -n1 | grep -qE '^#!.*\b(bash|sh)\b' && scripts+=("$f")
done < <(git ls-files | grep -E '^(bin|scripts)/' || true)

# Dedupe.
if [[ ${#scripts[@]} -gt 0 ]]; then
  mapfile -t scripts < <(printf '%s\n' "${scripts[@]}" | sort -u)
fi

if [[ ${#scripts[@]} -eq 0 ]]; then
  exit 0
fi

shellcheck --severity=warning --shell=bash "${scripts[@]}"
