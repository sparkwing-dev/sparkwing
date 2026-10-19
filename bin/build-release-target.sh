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
build_dir="$(mktemp -d)"
created_tag=
cleanup() {
  rm -rf "$build_dir"
  if [ -n "$created_tag" ]; then git tag -d "$TAG" >/dev/null; fi
}
trap cleanup EXIT
# safety: -trimpath drops -X from build info, so the engine's version check reads
# the module version Go stamps from an exact tag on a clean checkout. A tag this
# script creates is removed on exit so it never reaches a push.
if tagged="$(git rev-parse -q --verify "refs/tags/$TAG^{commit}")"; then
  if [ "$tagged" != "$(git rev-parse HEAD)" ]; then
    echo "tag $TAG does not name the checked-out commit" >&2
    exit 2
  fi
else
  git tag "$TAG"
  created_tag=1
fi
# safety: only a QA pre-release may trust the QA key and origin.
want_qatrust=
if [[ "$TAG" =~ -qa\.[1-9][0-9]*$ ]]; then want_qatrust=1; fi
mkdir -p dist
packages=()
for binary in "${binaries[@]}"; do
  packages+=("./cmd/${binary}")
done
CGO_ENABLED=0 GOWORK=off go build -trimpath \
  -ldflags="-s -w -X main.Version=${TAG}" \
  -o "$build_dir/" "${packages[@]}"
for binary in "${binaries[@]}"; do
  info="$(go version -m "$build_dir/$binary${ext}")"
  stamped="$(awk '$1 == "mod" { print $3 }' <<<"$info")"
  if [ "$stamped" != "$TAG" ]; then
    echo "$binary reports version ${stamped:-none}, want $TAG; build from a clean checkout with VCS stamping on" >&2
    exit 1
  fi
  tags="$(awk '$1 == "build" && $2 ~ /^-tags=/ { sub(/^-tags=/, "", $2); print $2 }' <<<"$info")"
  has_qatrust=
  if [[ ",$tags," == *,qatrust,* ]]; then has_qatrust=1; fi
  if [ "$has_qatrust" != "$want_qatrust" ]; then
    echo "$binary was built with tags '${tags}'; only a vX.Y.Z-qa.N release carries qatrust" >&2
    exit 1
  fi
done
for binary in "${binaries[@]}"; do
  mv "$build_dir/$binary${ext}" "dist/${binary}-${GOOS}-${GOARCH}${ext}"
done
