#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
go -C "$REPO_ROOT" run ./internal/sdkref "$REPO_ROOT" "$REPO_ROOT/docs"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/sdk-reference.md + docs/sdk-<name>.md + synced pkg/docs/mirror"
