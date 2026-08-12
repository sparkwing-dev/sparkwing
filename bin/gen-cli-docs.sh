#!/usr/bin/env bash
# Regenerate the CLI reference -- docs/cli-reference.md (index) plus one
# docs/cli-<group>.md page per top-level command group -- from the CLI
# command registry, then re-sync the embedded mirror. These pages are
# GENERATED -- never edit them by hand; change the command/flag
# definitions in cmd/sparkwing/help_registry.go and rerun this. The
# pre-push docs-generated gate fails until the committed files match.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
go -C "$REPO_ROOT" run ./cmd/sparkwing commands -o markdown --split-dir "$REPO_ROOT/docs"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/cli-reference.md + docs/cli-<group>.md + synced pkg/docs/mirror"
