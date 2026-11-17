#!/usr/bin/env bash
# usage: check-generated-pages.sh GENERATED_DIR GLOB HEADER_PATTERN REGENERATE_COMMAND
# Fails when a freshly generated page differs from docs/, or when docs/ keeps
# a page carrying the generator's header that the generator no longer writes.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
generated="$1"
glob="$2"
header="$3"
regenerate="$4"

fail=0
for f in "$generated"/*.md; do
  if ! git -C "$REPO_ROOT" ls-files --error-unmatch -- ":(literal)docs/$(basename "$f")" >/dev/null 2>&1; then
    echo "generated page is not tracked: docs/$(basename "$f")" >&2
    fail=1
    continue
  fi
  diff -u "$REPO_ROOT/docs/$(basename "$f")" "$f" || fail=1
done
while IFS= read -r -d '' file; do
  name="${file#docs/}"
  [[ "$name" != */* ]] || continue
  # safety: the caller supplies a filename pattern, so quoting would disable stale-page matching.
  # shellcheck disable=SC2254
  case "$name" in $glob) ;; *) continue ;; esac
  f="$REPO_ROOT/$file"
  if [ ! -e "$generated/$name" ] && head -n1 "$f" | grep -q "$header"; then
    echo "stale generated page: docs/$(basename "$f")"
    fail=1
  fi
done < <(git -C "$REPO_ROOT" ls-files -z -- docs)
if [ "$fail" -ne 0 ]; then
  echo "generated pages are stale; run $regenerate" >&2
  exit 1
fi
