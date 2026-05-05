#!/usr/bin/env bash
# Build the dashboard and copy its output into internal/web/next-out/
# for the //go:embed in cmd/sparkwing-web. Run this before
# 'go build ./cmd/sparkwing-web' if you want a populated dashboard.
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
