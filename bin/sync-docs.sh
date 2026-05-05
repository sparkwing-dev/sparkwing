#!/usr/bin/env bash
# Mirror /docs/ -> pkg/docs/content/ so the embed picks up new
# markdown. Run after editing anything under /docs/. The
# TestPkgDocsContentMatchesDocsRoot guard test will fail CI
# until this is run.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
SRC="$REPO_ROOT/docs"
DST="$REPO_ROOT/pkg/docs/content"

if [ ! -d "$SRC" ]; then
  echo "sync-docs: $SRC not found" >&2
  exit 1
fi

rm -rf "$DST"
cp -r "$SRC" "$DST"

echo "synced $SRC -> $DST"
