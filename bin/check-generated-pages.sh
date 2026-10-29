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
  diff -u "$REPO_ROOT/docs/$(basename "$f")" "$f" || fail=1
done
for f in "$REPO_ROOT"/docs/$glob; do
  if [ ! -e "$generated/$(basename "$f")" ] && head -n1 "$f" | grep -q "$header"; then
    echo "stale generated page: docs/$(basename "$f")"
    fail=1
  fi
done
if [ "$fail" -ne 0 ]; then
  echo "generated pages are stale; run $regenerate" >&2
  exit 1
fi
