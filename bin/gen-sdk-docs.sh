#!/usr/bin/env bash
# Regenerate docs/sdk-reference.md from the `sparkwing` package via
# go/doc, then re-sync the embedded mirror. docs/sdk-reference.md is
# GENERATED -- never edit it by hand; change the exported API / its
# godoc and rerun this. The pre-push docs-generated gate fails until
# the committed file matches.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
go -C "$REPO_ROOT" run ./internal/sdkref "$REPO_ROOT" > "$REPO_ROOT/docs/sdk-reference.md"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/sdk-reference.md + synced pkg/docs/mirror"
