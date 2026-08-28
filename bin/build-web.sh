#!/usr/bin/env bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
cd "$HERE/web"
npm ci --ignore-scripts --no-audit
npm run build
rm -rf "$HERE/internal/web/next-out"
mkdir -p "$HERE/internal/web/next-out"
cp -R "$HERE/web/out/." "$HERE/internal/web/next-out/"
touch "$HERE/internal/web/next-out/.gitkeep"
echo "==> next-out populated"
