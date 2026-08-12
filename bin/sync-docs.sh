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
#
# `--check` reports drift instead of fixing it: it writes nothing and
# exits non-zero listing the files that differ. That is the mode a
# reviewer asked to "run the drift check" wants, and the mode the
# pre-commit docs-mirror gate runs -- a check that repairs the tree it
# was pointed at cannot report on it, and until this flag existed an
# unknown argument was ignored and a real sync happened instead.

set -euo pipefail

usage() {
  cat <<'EOF'
usage: bash bin/sync-docs.sh [--check]

  (no flags)  copy docs/ -> pkg/docs/mirror/ and CHANGELOG.md ->
              pkg/docs/changelog.md, replacing whatever is there
  --check     report drift and exit non-zero; write nothing
  -h, --help  this message
EOF
}

check=0
for arg in "$@"; do
  case "$arg" in
    --check) check=1 ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "sync-docs: unknown argument: $arg" >&2
      usage >&2
      exit 2
      ;;
  esac
done

REPO_ROOT="$(git rev-parse --show-toplevel)"
SRC="$REPO_ROOT/docs"
DST="$REPO_ROOT/pkg/docs/mirror"
CHANGELOG_SRC="$REPO_ROOT/CHANGELOG.md"
CHANGELOG_DST="$REPO_ROOT/pkg/docs/changelog.md"

if [ ! -d "$SRC" ]; then
  echo "sync-docs: $SRC not found" >&2
  exit 1
fi

if [ "$check" -eq 1 ]; then
  # The sync is `rm -rf DST; cp -r SRC DST`, so the would-be output is
  # docs/ byte for byte and a recursive diff is the whole comparison.
  # Run it from the repo root so the report names the paths a reader
  # would edit rather than absolute ones.
  cd "$REPO_ROOT"
  drift=""
  if [ ! -d "pkg/docs/mirror" ]; then
    drift="pkg/docs/mirror/ is missing (the mirror has never been generated)"
  else
    drift="$(diff -rq docs pkg/docs/mirror || true)"
  fi
  if ! cmp -s CHANGELOG.md pkg/docs/changelog.md; then
    changelog_drift="CHANGELOG.md and pkg/docs/changelog.md differ"
    if [ -n "$drift" ]; then
      drift="$drift
$changelog_drift"
    else
      drift="$changelog_drift"
    fi
  fi
  if [ -n "$drift" ]; then
    echo "sync-docs --check: the embedded mirror has drifted from the source:" >&2
    echo "$drift" | sed 's/^/  /' >&2
    echo "Fix: bash bin/sync-docs.sh (edit docs/ and CHANGELOG.md, never the mirror)" >&2
    exit 1
  fi
  echo "sync-docs --check: pkg/docs/mirror matches docs/, pkg/docs/changelog.md matches CHANGELOG.md"
  exit 0
fi

rm -rf "$DST"
cp -r "$SRC" "$DST"

cp "$CHANGELOG_SRC" "$CHANGELOG_DST"

echo "synced $SRC -> $DST"
echo "synced $CHANGELOG_SRC -> $CHANGELOG_DST"
