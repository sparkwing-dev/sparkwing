#!/usr/bin/env bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
reuse=0
if [[ "${1:-}" == --reuse && $# == 1 ]]; then
  reuse=1
elif (( $# != 0 )); then
  echo "usage: build-web.sh [--reuse]" >&2
  exit 2
fi
# shellcheck source=bin/web-build-lock.sh
source "$HERE/bin/web-build-lock.sh"
sparkwing_lock_web_build "$HERE"
export NODE_ENV=production
proof="$HERE/bin/web-build-proof.mjs"
receipt="$HERE/internal/web/.build-state/receipt.json"
if (( reuse && SPARKWING_WEB_BUILD_LOCKED )) && node "$proof" check "$HERE"; then
  echo "==> reusing validated next-out"
  exit 0
fi
rm -f "$receipt"
before=""
if (( SPARKWING_WEB_BUILD_LOCKED )); then
  before="$(node "$proof" inputs "$HERE")" || before=""
fi
rm -rf "$HERE/web/out"
(
  cd "$HERE/web"
  npm ci --include=dev --ignore-scripts --no-audit
  npm run build
)
if [[ ! -f "$HERE/web/out/index.html" ]]; then
  echo "web build did not produce a static index.html" >&2
  exit 1
fi
rm -rf "$HERE/internal/web/next-out"
mkdir -p "$HERE/internal/web/next-out"
cp -R "$HERE/web/out/." "$HERE/internal/web/next-out/"
touch "$HERE/internal/web/next-out/.gitkeep"
if [[ -n "$before" ]]; then
  if ! node "$proof" record "$HERE" "$before"; then
    echo "web build inputs changed or proof unavailable; refusing publication" >&2
    exit 1
  fi
fi
echo "==> next-out populated"
