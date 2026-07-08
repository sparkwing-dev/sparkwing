#!/usr/bin/env bash
# Build the dashboard SPA and copy its output into
# internal/web/next-out/. Two binaries embed it via
# //go:embed all:next-out:
#   - cmd/sparkwing (powers `sparkwing dashboard start`)
#   - cmd/sparkwing-web (cluster dashboard pod)
# Run this before `go build` on either if you want a populated bundle.
# bin/install.sh and .github/workflows/release.yaml both call this so
# every laptop install + released artifact ships the current dashboard.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
cd "$HERE/web"
npm ci
npm run build
rm -rf "$HERE/internal/web/next-out"
mkdir -p "$HERE/internal/web/next-out"
cp -R "$HERE/web/out/." "$HERE/internal/web/next-out/"
touch "$HERE/internal/web/next-out/.gitkeep"
echo "==> next-out populated"
