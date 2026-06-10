#!/usr/bin/env bash
# Regenerate docs/api-reference.md from the controller + logs-service
# route registrations, then re-sync the embedded mirror.
# docs/api-reference.md is GENERATED -- never edit it by hand; change
# the routes in pkg/controller/server.go or pkg/logs/server.go and
# rerun this. The pre-push docs-generated gate fails until the committed
# file matches.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
go -C "$REPO_ROOT" run ./internal/apiref "$REPO_ROOT" > "$REPO_ROOT/docs/api-reference.md"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/api-reference.md + synced pkg/docs/mirror"
