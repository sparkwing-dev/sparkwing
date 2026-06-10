#!/usr/bin/env bash
# Regenerate docs/cli-reference.md from the CLI command registry, then
# re-sync the embedded mirror. docs/cli-reference.md is GENERATED --
# never edit it by hand; change the command/flag definitions in
# cmd/sparkwing/help_registry.go and rerun this. The pre-push
# docs-generated gate fails until the committed file matches.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
go -C "$REPO_ROOT" run ./cmd/sparkwing commands -o markdown > "$REPO_ROOT/docs/cli-reference.md"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/cli-reference.md + synced pkg/docs/mirror"
