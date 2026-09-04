#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
go -C "$REPO_ROOT" run ./internal/configref "$REPO_ROOT" > "$REPO_ROOT/docs/config-reference.md"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/config-reference.md + synced pkg/docs/mirror"
