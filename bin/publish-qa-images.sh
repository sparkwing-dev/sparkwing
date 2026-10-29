#!/usr/bin/env bash
# Publishes a QA pre-release's controller and runner images from the signed
# linux binaries in <src>/dist: one image per architecture tagged <version>-<arch>,
# and one linux/amd64 + linux/arm64 index tagged <version>. The architecture tags
# keep each child tagged in a registry that expires untagged images.
#
# usage: bin/publish-qa-images.sh <src> <registry> <vX.Y.Z-qa.N> <sha>
#
# The arm64 runtime stages run apt and apk, so the buildx builder must run
# linux/arm64 natively or through binfmt emulation.
set -euo pipefail

if (( $# != 4 )); then
  echo "usage: bin/publish-qa-images.sh <src> <registry> <vX.Y.Z-qa.N> <sha>" >&2
  exit 2
fi
src=$1 registry=${2%/} version=$3 sha=$4
arches=(amd64 arm64)

for arch in "${arches[@]}"; do
  for binary in sparkwing-controller sparkwing-runner; do
    if [[ ! -s "$src/dist/$binary-linux-$arch" ]]; then
      echo "missing $src/dist/$binary-linux-$arch; build the release matrix first" >&2
      exit 1
    fi
  done
done

for binary in sparkwing-controller sparkwing-runner; do
  recipe=build/Dockerfile.binary
  if [[ "$binary" == sparkwing-runner ]]; then recipe=build/Dockerfile.runner; fi
  image=$registry/$binary
  children=()
  for arch in "${arches[@]}"; do
    docker buildx build --file "$src/$recipe" --target release \
      --platform "linux/$arch" \
      --build-arg "BINARY=$binary" \
      --build-arg "SPARKWING_IMAGE_REFRESH=$version" \
      --label "org.opencontainers.image.version=$version" \
      --label "org.opencontainers.image.revision=$sha" \
      --label "org.opencontainers.image.source=https://github.com/sparkwing-dev/sparkwing" \
      --tag "$image:$version-$arch" --push "$src"
    children+=("$image:$version-$arch")
  done
  docker buildx imagetools create --tag "$image:$version" "${children[@]}"
  platforms=$(docker buildx imagetools inspect --raw "$image:$version" |
    jq -r '[.manifests[].platform | select(.os == "linux") | .architecture] | unique | join(",")')
  if [[ "$platforms" != "amd64,arm64" ]]; then
    echo "$image:$version lists linux platforms '$platforms', want amd64,arm64" >&2
    exit 1
  fi
  echo "==> $image:$version (linux/amd64, linux/arm64)"
done
