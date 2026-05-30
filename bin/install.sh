#!/usr/bin/env bash
# Build the sparkwing CLI and install to ~/.local/bin so any previously
# installed copy is replaced.
#
# Only `sparkwing` lands on laptops. Cluster-side binaries (controller,
# runner, cache, logs, web) ship as Docker images. The old standalone
# `sparkwing-local-ws` daemon is superseded by `sparkwing dashboard
# start` -- if a stale copy is present in $DEST we delete it so the
# user's PATH stops resolving the older binary.
#
# Rebuilds the dashboard SPA (web/ -> internal/web/next-out) before
# the Go build so the embedded bundle is always current. Set
# SKIP_WEB_BUILD=1 to skip when iterating only on Go code and the
# bundle is already populated.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="${SPARKWING_INSTALL_BIN:-$HOME/.local/bin}"
mkdir -p "$DEST"

# GOPRIVATE so freshly-tagged sparks/sdk modules resolve directly from
# GitHub if proxy lags.
export GOPRIVATE='github.com/sparkwing-dev/*'

if [ "${SKIP_WEB_BUILD:-0}" = "1" ]; then
  echo "SKIP_WEB_BUILD=1 set; using existing internal/web/next-out/ as-is"
else
  bash "$ROOT/bin/build-web.sh"
fi

echo "build sparkwing"
go -C "$ROOT" build -o "$DEST/sparkwing" ./cmd/sparkwing

# Sweep deprecated / cluster-only binaries that prior install.sh
# revisions used to drop in $DEST. Silent if absent.
declare -a STALE=(
  sparkwing-local-ws
  sparkwing-web
  sparkwing.dev
  sparkwing.predeploy
)
for s in "${STALE[@]}"; do
  if [ -e "$DEST/$s" ]; then
    rm -f "$DEST/$s"
    echo "removed stale $DEST/$s"
  fi
done

echo
echo "Installed to $DEST:"
ls -1 "$DEST"/sparkwing*
