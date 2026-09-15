#!/usr/bin/env bash
set -euo pipefail

mkdir -p scanned-image-digests
for binary in sparkwing-controller sparkwing-runner sparkwing-cache sparkwing-logs sparkwing-web; do
  image="ghcr.io/sparkwing-dev/$binary"
  sources=()
  for arch in amd64 arm64; do
    digest="$(jq -er '."containerimage.digest"' "image-platform-digests/$binary-$arch.json")"
    [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]
    sources+=("$image@$digest")
  done
  # Command substitution removes the display newline, leaving the exact manifest bytes.
  manifest="$(docker buildx imagetools create --dry-run "${sources[@]}")"
  digest="sha256:$(printf '%s' "$manifest" | sha256sum | cut -d ' ' -f 1)"
  docker buildx imagetools create --tag "$image@$digest" "${sources[@]}"
  resolved="$(docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$image@$digest")"
  test "$resolved" = "$digest"
  printf '%s\n' "$digest" >"scanned-image-digests/$binary"
done
