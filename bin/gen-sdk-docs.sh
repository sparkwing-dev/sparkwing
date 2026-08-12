#!/usr/bin/env bash
# Regenerate the SDK reference -- docs/sdk-reference.md (root package +
# subpackage index) plus one docs/sdk-<name>.md per subpackage -- from
# the `sparkwing` package via go/doc, then re-sync the embedded mirror.
# These pages are GENERATED -- never edit them by hand; change the
# exported API / its godoc and rerun this. The pre-push docs-generated
# gate fails until the committed files match.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
go -C "$REPO_ROOT" run ./internal/sdkref "$REPO_ROOT" "$REPO_ROOT/docs"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/sdk-reference.md + docs/sdk-<name>.md + synced pkg/docs/mirror"
