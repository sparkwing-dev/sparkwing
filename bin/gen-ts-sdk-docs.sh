#!/usr/bin/env bash
# Regenerates docs/sdk-typescript-reference.md from sdk/typescript/index.d.ts.
# The TypeScript SDK suite's reference test fails when the committed page is stale.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [ ! -d "$REPO_ROOT/sdk/typescript/node_modules/typescript" ]; then
  npm --prefix "$REPO_ROOT/sdk/typescript" ci --ignore-scripts --no-audit --no-fund >/dev/null
fi
node "$REPO_ROOT/sdk/typescript/tools/reference.ts" >"$REPO_ROOT/docs/sdk-typescript-reference.md"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/sdk-typescript-reference.md + synced pkg/docs/mirror"
