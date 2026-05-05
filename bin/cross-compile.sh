#!/usr/bin/env bash
# Cross-compile sparkwing CLI binaries for the four common platforms.
# Output goes to dist/ in the cli repo for upload to a GitHub release.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
mkdir -p "$DIST"
rm -f "$DIST"/*

# Tester-relevant binaries:
# - sparkwing: the operator/dev CLI (the main one)
# - sparkwing-local-ws: laptop dashboard server (so 'sparkwing dashboard start'
#   can spawn it as a subprocess if installed alongside)
declare -a BINS=(sparkwing sparkwing-local-ws)

# Platform matrix.
declare -a PLATFORMS=(
  "darwin/arm64"
  "darwin/amd64"
  "linux/arm64"
  "linux/amd64"
)

# Module path-aware build using cli's go-modules.
export GOPRIVATE=github.com/sparkwing-dev/*

for plat in "${PLATFORMS[@]}"; do
  goos="${plat%/*}"
  goarch="${plat##*/}"
  for bin in "${BINS[@]}"; do
    out="$DIST/${bin}-${goos}-${goarch}"
    echo "build $out"
    GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go -C "$ROOT" build \
      -ldflags="-s -w" \
      -o "$out" \
      "./cmd/$bin"
  done
done

# Build a checksums file so install scripts can verify downloads.
( cd "$DIST" && sha256sum -- * > SHA256SUMS )

echo
echo "Built artifacts:"
ls -lh "$DIST"
