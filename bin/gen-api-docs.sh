#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
go -C "$REPO_ROOT" run ./internal/apiref "$REPO_ROOT" > "$REPO_ROOT/docs/api-reference.md"

spec="$REPO_ROOT/api/openapi.yaml"
rewritten="$(mktemp)"
trap 'rm -f "$rewritten"' EXIT
go -C "$REPO_ROOT" run ./internal/apispec "$REPO_ROOT" "$spec" > "$rewritten"
mv "$rewritten" "$spec"

bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/api-reference.md + api/openapi.yaml + synced pkg/docs/mirror"
