#!/usr/bin/env bash

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
