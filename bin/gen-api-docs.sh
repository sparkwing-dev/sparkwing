#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
go -C "$REPO_ROOT" run ./internal/apiref "$REPO_ROOT" > "$REPO_ROOT/docs/api-reference.md"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/api-reference.md + synced pkg/docs/mirror"
