#!/usr/bin/env bash
# Build the sparkwing CLI binaries and install to ~/.local/bin so any
# previously installed copy is replaced.
#
# Rebuilds the dashboard SPA (web/ -> internal/web/next-out) before
# the Go build so the embedded bundle is always current. Set
# SKIP_WEB_BUILD=1 to skip when iterating only on Go code and the
# bundle is already populated.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="${SPARKWING_INSTALL_BIN:-$HOME/.local/bin}"
mkdir -p "$DEST"

declare -a BINS=(
  sparkwing
  sparkwing-local-ws
  sparkwing-web
)

# GOPRIVATE so freshly-tagged sparks/sdk modules resolve directly from
# GitHub if proxy lags.
export GOPRIVATE='github.com/sparkwing-dev/*'

if [ "${SKIP_WEB_BUILD:-0}" = "1" ]; then
  echo "SKIP_WEB_BUILD=1 set; using existing internal/web/next-out/ as-is"
else
  bash "$ROOT/bin/build-web.sh"
fi

for b in "${BINS[@]}"; do
  echo "build $b"
  go -C "$ROOT" build -o "$DEST/$b" "./cmd/$b"
done

echo
echo "Installed to $DEST:"
ls -1 "$DEST"/sparkwing*
