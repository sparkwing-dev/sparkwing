#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if ! command -v shellcheck >/dev/null 2>&1; then
  echo "check-shell: shellcheck not installed (brew install shellcheck)" >&2
  exit 1
fi

scripts=()
while IFS= read -r -d '' f; do
  include=
  case "$f" in
    *.sh) include=1 ;;
    bin/*|scripts/*)
      if [[ -f "$f" ]] && head -c 64 "$f" 2>/dev/null | head -n1 | grep -qE '^#!.*\b(bash|sh)\b'; then
        include=1
      fi
      ;;
  esac
  if [[ -n "$include" ]]; then
    scripts+=("$f")
  fi
done < <(git ls-files -z | LC_ALL=C sort -zu)

# safety: this repository tracks dozens of scripts, so an empty list is a
# broken enumeration, not a clean tree. Passing on it would report a verdict
# that judged no script at all.
if [[ ${#scripts[@]} -eq 0 ]]; then
  echo "check-shell: no tracked shell scripts found; git ls-files returned nothing here" >&2
  exit 1
fi

shellcheck --severity=warning --shell=bash "${scripts[@]}"
