#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [[ ! -d .apidiff ]]; then
  echo "check-api-snapshot: .apidiff/ baseline missing; run bash bin/regen-api-snapshot.sh first" >&2
  exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

go run ./cmd/apidiff "$tmp" >/dev/null

if diff -r -q .apidiff "$tmp" >/dev/null 2>&1; then
  exit 0
fi

echo "API surface drift detected." >&2
echo "" >&2
diff -r -u .apidiff "$tmp" >&2 || true
echo "" >&2
echo "How to fix:" >&2
echo "" >&2
echo "  If this is an intentional API change:" >&2
echo "    1. Update CHANGELOG.md under [Unreleased]" >&2
echo "       (Added / Changed / Removed / Deprecated -- see VERSIONING.md)" >&2
echo "    2. Regenerate the snapshot:  bash bin/regen-api-snapshot.sh" >&2
echo "    3. git add .apidiff/" >&2
echo "" >&2
echo "  If unintentional, revert the surface change in your branch." >&2
exit 1
