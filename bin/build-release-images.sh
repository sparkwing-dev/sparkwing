#!/usr/bin/env bash
set -euo pipefail

: "${GOARCH:?GOARCH is required}" "${TAG:?TAG is required}" "${SPARKWING_IMAGE_REFRESH:?refresh is required}"
case "$GOARCH" in amd64|arm64) ;; *) exit 2 ;; esac
event="${GITHUB_EVENT_PATH:?GitHub event metadata is required}"
title="$(jq -r '.repository.name // ""' "$event")"
description="$(jq -r '.repository.description // ""' "$event")"
license="$(jq -r '.repository.license.spdx_id // ""' "$event")"
created="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
mkdir -p image-platform-digests
for binary in sparkwing-controller sparkwing-runner sparkwing-cache sparkwing-logs sparkwing-web; do
  recipe=build/Dockerfile.binary
  if [ "$binary" = sparkwing-runner ]; then recipe=build/Dockerfile.runner; fi
  image="ghcr.io/sparkwing-dev/$binary"
  docker buildx build --file "$recipe" --target release \
    --platform "linux/$GOARCH" \
    --build-arg "BINARY=$binary" \
    --build-arg "SPARKWING_IMAGE_REFRESH=$SPARKWING_IMAGE_REFRESH" \
    --label "org.opencontainers.image.version=$TAG" \
    --label "org.opencontainers.image.revision=${SOURCE_SHA:?source SHA is required}" \
    --label "org.opencontainers.image.source=https://github.com/sparkwing-dev/sparkwing" \
    --label "org.opencontainers.image.url=https://github.com/sparkwing-dev/sparkwing" \
    --label "org.opencontainers.image.title=$title" \
    --label "org.opencontainers.image.description=$description" \
    --label "org.opencontainers.image.licenses=$license" \
    --label "org.opencontainers.image.created=$created" \
    --output "type=image,name=$image,push-by-digest=true,name-canonical=true,push=true" \
    --metadata-file "image-platform-digests/$binary-$GOARCH.json" .
done
