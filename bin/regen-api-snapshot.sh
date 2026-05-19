#!/usr/bin/env bash
# Regenerate the checked-in API surface snapshots under .apidiff/.
# Run this whenever you intentionally change a public API; commit the
# updated .apidiff/ files in the same PR as the API change.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

mkdir -p .apidiff
go run ./cmd/apidiff .apidiff
echo "Wrote API snapshots to .apidiff/"
