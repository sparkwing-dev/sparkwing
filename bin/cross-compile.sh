#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
mkdir -p "$DIST"
rm -f "$DIST"/*

declare -a BINS=(sparkwing)

declare -a PLATFORMS=(
  "darwin/arm64"
  "darwin/amd64"
  "linux/arm64"
  "linux/amd64"
)

export GOPRIVATE='github.com/sparkwing-dev/*'

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

( cd "$DIST" && sha256sum -- * > SHA256SUMS )

echo
echo "Built artifacts:"
ls -lh "$DIST"
