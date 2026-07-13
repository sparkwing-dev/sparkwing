#!/usr/bin/env bash
# Mirror docs/ (the canonical source) -> pkg/docs/mirror/ so the CLI
# embed picks up new markdown. docs/ is also consumed by the
# sparkwing-product website build; pkg/docs/mirror/ is generated --
# never edit it directly. Run this after editing anything under docs/;
# the pre-commit gate and the TestPkgDocsContentMatchesDocsRoot guard
# test fail until it is run.
#
# Also copies CHANGELOG.md into pkg/docs/changelog.md so the CLI can
# embed it as the `changelog` docs topic. Run this after editing
# CHANGELOG.md too; the TestEmbeddedChangelogMatchesRoot guard test
# fails until it is run.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
SRC="$REPO_ROOT/docs"
DST="$REPO_ROOT/pkg/docs/mirror"

if [ ! -d "$SRC" ]; then
  echo "sync-docs: $SRC not found" >&2
  exit 1
fi

rm -rf "$DST"
cp -r "$SRC" "$DST"

cp "$REPO_ROOT/CHANGELOG.md" "$REPO_ROOT/pkg/docs/changelog.md"

echo "synced $SRC -> $DST"
echo "synced $REPO_ROOT/CHANGELOG.md -> $REPO_ROOT/pkg/docs/changelog.md"
