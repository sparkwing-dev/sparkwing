#!/usr/bin/env bash
set -euo pipefail

: "${GOOS:?GOOS is required}" "${GOARCH:?GOARCH is required}" "${TAG:?TAG is required}"
case "$GOOS/$GOARCH" in
  linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64|windows/arm64) ;;
  *) echo "unsupported release target $GOOS/$GOARCH" >&2; exit 2 ;;
esac
binaries=(sparkwing sparkwing-cache sparkwing-controller sparkwing-runner sparkwing-logs sparkwing-web)
ext=""
if [ "$GOOS" = windows ]; then
  binaries=(sparkwing sparkwing-runner)
  ext=.exe
fi
mkdir -p dist
build_dir="$(mktemp -d)"
trap 'rm -rf "$build_dir"' EXIT
packages=()
for binary in "${binaries[@]}"; do
  packages+=("./cmd/${binary}")
done
CGO_ENABLED=0 GOWORK=off go build -trimpath \
  -ldflags="-s -w -X main.Version=${TAG}" \
  -o "$build_dir/" "${packages[@]}"
for binary in "${binaries[@]}"; do
  mv "$build_dir/$binary${ext}" "dist/${binary}-${GOOS}-${GOARCH}${ext}"
done
