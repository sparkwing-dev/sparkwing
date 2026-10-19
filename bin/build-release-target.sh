#!/usr/bin/env bash
set -euo pipefail

: "${GOOS:?GOOS is required}" "${GOARCH:?GOARCH is required}" "${TAG:?TAG is required}"
case "$GOOS/$GOARCH" in
  linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64|windows/arm64) ;;
  *) echo "unsupported release target $GOOS/$GOARCH" >&2; exit 2 ;;
esac
binaries=(sparkwing sparkwing-controller sparkwing-runner)
ext=""
if [ "$GOOS" = windows ]; then
  binaries=(sparkwing sparkwing-runner)
  ext=.exe
fi
# safety: -trimpath drops -X from build info, so the engine's version check reads
# the module version Go stamps from an exact tag on a clean checkout.
if tagged="$(git rev-parse -q --verify "refs/tags/$TAG^{commit}")"; then
  if [ "$tagged" != "$(git rev-parse HEAD)" ]; then
    echo "tag $TAG does not name the checked-out commit" >&2
    exit 2
  fi
else
  git tag "$TAG"
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
  stamped="$(go version -m "$build_dir/$binary${ext}" | awk '$1 == "mod" { print $3 }')"
  if [ "$stamped" != "$TAG" ]; then
    echo "$binary reports version ${stamped:-none}, want $TAG; build from a clean checkout with VCS stamping on" >&2
    exit 1
  fi
  mv "$build_dir/$binary${ext}" "dist/${binary}-${GOOS}-${GOARCH}${ext}"
done
