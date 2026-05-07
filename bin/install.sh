#!/usr/bin/env bash
# Build the sparkwing CLI binaries and install to ~/.local/bin so any
# previously installed copy is replaced.
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
export GOPRIVATE=github.com/sparkwing-dev/*

for b in "${BINS[@]}"; do
  echo "build $b"
  go -C "$ROOT" build -o "$DEST/$b" "./cmd/$b"
done

# 'wing' alias (run pipelines shortcut). The CLI's main routes both
# bare-name invocations through the same entry point.
ln -sf "$DEST/sparkwing" "$DEST/wing"

echo
echo "Installed to $DEST:"
ls -1 "$DEST"/sparkwing* "$DEST"/wing
