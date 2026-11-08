#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [[ $# -gt 0 ]]; then
  if [[ "$1" == "-h" || "$1" == "--help" ]]; then
    echo "usage: bash bin/check-changelog.sh"
    echo "Require a nonempty docs/changes/<slug>.md fragment for covered surfaces."
    exit 0
  fi
  echo "check-changelog: unknown argument: $1" >&2
  exit 2
fi

if awk '
 /^## \[Unreleased\]/ { section=1; next }
 section && /^## / { section=0 }
 section && /[^[:space:]]/ { found=1 }
 END { exit !found }
' CHANGELOG.md; then
  echo "check-changelog: move [Unreleased] entries to docs/changes/<slug>.md" >&2
  exit 1
fi
if [[ -f docs/migrations/_unreleased.md ]] && rg -q '^## ' docs/migrations/_unreleased.md; then
  echo "check-changelog: move migration sections to docs/migrations/unreleased/<slug>.md" >&2
  exit 1
fi

base="${BASE_REF:-}"
if [[ -z "$base" ]]; then
  if git rev-parse --verify --quiet origin/main >/dev/null; then
    base="origin/main"
  elif git rev-parse --verify --quiet HEAD~1 >/dev/null; then
    base="HEAD~1"
  else
    echo "check-changelog: no comparison base (no origin/main, no HEAD~1); skipping"
    exit 0
  fi
fi

changed=()
while IFS= read -r -d '' f; do
  changed+=("$f")
done < <(
  {
    git diff --name-only -z "$base"...HEAD
    git diff --name-only -z HEAD
    git diff --name-only -z --cached
    git ls-files --others --exclude-standard -z
  } | sort -zu
)

if [[ ${#changed[@]} -eq 0 ]]; then
  exit 0
fi

is_covered() {
  local f="$1"
  case "$f" in
    *_test.go)            return 1 ;;
    */testdata/*)         return 1 ;;
    internal/runners/k8s/k8s.go) return 0 ;;
    internal/*)           return 1 ;;
    docs/*|examples/*)    return 1 ;;
    pkg/docs/mirror/*|pkg/docs/changelog.md) return 1 ;;
    build/*)              return 1 ;;
    charts/*/values.yaml|charts/*/values.schema.json) return 0 ;;
    charts/*/templates/*|charts/*/charts/*.tgz) return 0 ;;
    charts/*/Chart.yaml|charts/*/Chart.lock) return 0 ;;
    charts/*|install/*)   return 1 ;;
    web/*|node_modules/*) return 1 ;;
    pkg/*.go|pkg/*/*)     return 0 ;;
    sparkwing/*.go|sparkwing/*/*) return 0 ;;
    cmd/*/*.go)           return 0 ;;
  esac
  return 1
}

covered_changes=()
fragment_added=false
for f in "${changed[@]}"; do
  [[ -z "$f" ]] && continue
  if [[ "$f" == docs/changes/*.md && -s "$f" ]]; then
    added=$(
      {
        git diff "$base"...HEAD -- "$f"
        git diff HEAD -- "$f"
        if ! git ls-files --error-unmatch -- "$f" >/dev/null 2>&1; then
          sed 's/^/+/' "$f"
        fi
      } | awk '/^\+[^+]/ && $0 !~ /^\+[[:space:]]*$/ { print }'
    )
    if [[ -n "$added" ]] && rg -q '^- ' "$f"; then fragment_added=true; fi
  fi
  if is_covered "$f"; then
    covered_changes+=("$f")
  fi
done

if [[ ${#covered_changes[@]} -eq 0 ]]; then
  exit 0
fi

if [[ "$fragment_added" == "true" ]]; then
  exit 0
fi

echo "check-changelog: docs/changes/<slug>.md entry required" >&2
echo "" >&2
echo "Files changed on covered surfaces (per VERSIONING.md):" >&2
for f in "${covered_changes[@]}"; do
  echo "  $f" >&2
done
echo "" >&2
echo "Add a ### category and bullet in docs/changes/<slug>.md," >&2
echo "run bash bin/sync-docs.sh, then re-run. See VERSIONING.md for a breaking" >&2
echo "change and how to phrase entries." >&2
exit 1
