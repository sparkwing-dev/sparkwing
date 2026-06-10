#!/usr/bin/env bash
# Regenerate docs/config-reference.md from the sparkwing.yaml schema
# structs (pkg/pipelines, pkg/projectconfig), then re-sync the embedded
# mirror. docs/config-reference.md is GENERATED -- never edit it by
# hand; change the struct fields / field godoc and rerun this. The
# pre-push docs-generated gate fails until the committed file matches.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
go -C "$REPO_ROOT" run ./internal/configref "$REPO_ROOT" > "$REPO_ROOT/docs/config-reference.md"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/config-reference.md + synced pkg/docs/mirror"
