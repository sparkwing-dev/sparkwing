#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
go -C "$REPO_ROOT" run ./cmd/sparkwing commands -o markdown --split-dir "$REPO_ROOT/docs"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/cli-reference.md + docs/cli-<group>.md + synced pkg/docs/mirror"
