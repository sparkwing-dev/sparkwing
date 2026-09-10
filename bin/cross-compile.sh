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

# Same stamp install.sh writes: without -X main.Version an artifact reports the
# module version Go recorded rather than the commit it was built from.
BASE="$(git -C "$ROOT" tag -l 'v0.*' | sort -V | tail -1)"
[ -n "$BASE" ] || BASE="v0.0.0"
VERSION="$BASE-dev+$(git -C "$ROOT" rev-parse --short HEAD)"
if ! git -C "$ROOT" diff --quiet HEAD 2>/dev/null; then
  VERSION="$VERSION+dirty"
fi

for plat in "${PLATFORMS[@]}"; do
  goos="${plat%/*}"
  goarch="${plat##*/}"
  for bin in "${BINS[@]}"; do
    out="$DIST/${bin}-${goos}-${goarch}"
    echo "build $out"
    GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go -C "$ROOT" build \
      -trimpath \
      -ldflags="-s -w -X main.Version=$VERSION" \
      -o "$out" \
      "./cmd/$bin"
  done
done

( cd "$DIST" && sha256sum -- * > SHA256SUMS )

echo
echo "Built artifacts:"
ls -lh "$DIST"
