#!/usr/bin/env bash
# Verify CHANGELOG.md is updated when a commit touches a covered
# stability surface. See VERSIONING.md for the policy.
#
# Covered surfaces (any change to these requires a CHANGELOG entry):
#   - pkg/**         (public API)
#   - sparkwing/**   (author SDK)
#   - cmd/**/*.go    (CLI flag definitions; coarse-grained)
#
# Exclusions (no CHANGELOG entry required):
#   - **/*_test.go
#   - internal/**
#   - **/testdata/**
#   - docs/**, examples/**, bench/**, build/**, charts/**, install/**, web/**
#
# Comparison base:
#   - BASE_REF env var (CI sets this to the merge target)
#   - origin/main if it exists
#   - HEAD~1 otherwise (first commit on a fresh clone falls through)
#
# Pass: silent exit 0.
# Fail: prints offending files and exits 1.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

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

# Compare base..HEAD union'd with the working tree so the check fires
# on both committed work and staged-but-uncommitted changes.
changed=()
while IFS= read -r -d '' f; do
  changed+=("$f")
done < <(
  {
    git diff --name-only -z "$base"...HEAD
    git diff --name-only -z HEAD
    git diff --name-only -z --cached
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
    internal/*)           return 1 ;;
    docs/*|examples/*)    return 1 ;;
    bench/*|build/*)      return 1 ;;
    charts/*|install/*)   return 1 ;;
    web/*|node_modules/*) return 1 ;;
    pkg/*.go|pkg/*/*)     return 0 ;;
    sparkwing/*.go|sparkwing/*/*) return 0 ;;
    cmd/*/*.go)           return 0 ;;
  esac
  return 1
}

covered_changes=()
changelog_touched=false
for f in "${changed[@]}"; do
  [[ -z "$f" ]] && continue
  if [[ "$f" == "CHANGELOG.md" ]]; then
    changelog_touched=true
    continue
  fi
  if is_covered "$f"; then
    covered_changes+=("$f")
  fi
done

if [[ ${#covered_changes[@]} -eq 0 ]]; then
  exit 0
fi

if [[ "$changelog_touched" == "true" ]]; then
  # Verify the [Unreleased] section actually grew (not just whitespace).
  added=$(
    {
      git diff "$base"...HEAD -- CHANGELOG.md
      git diff HEAD -- CHANGELOG.md
      git diff --cached -- CHANGELOG.md
    } | awk '
      /^@@/ { in_hunk=1; next }
      in_hunk && /^\+[^+]/ { print substr($0, 2) }
    ' | grep -v '^[[:space:]]*$' || true
  )
  if [[ -n "$added" ]]; then
    exit 0
  fi
fi

echo "check-changelog: CHANGELOG.md update required" >&2
echo "" >&2
echo "Files changed on covered surfaces (per VERSIONING.md):" >&2
for f in "${covered_changes[@]}"; do
  echo "  $f" >&2
done
echo "" >&2
echo "Add an entry under the [Unreleased] section of CHANGELOG.md," >&2
echo "then re-run. See VERSIONING.md for what counts as a breaking" >&2
echo "change and how to phrase entries." >&2
exit 1
