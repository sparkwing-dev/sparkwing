#!/usr/bin/env bash
# Publishes a QA pre-release to https://qa-releases.sparkwing.dev, signed with
# the QA key, plus the Go SDK at that version under /goproxy, and the controller
# and runner images for linux/amd64 and linux/arm64 under one index per tag.
# Only builds made with the qatrust tag trust it, so production clients never
# see these releases.
#
# usage: bin/qa-release.sh <repo> <commit> <vX.Y.Z-qa.N>
#
# Reads the QA signing seed from SSM (AWS_PROFILE, default kikd-prod) straight
# into the signer's environment; it is never written to disk. Images go to
# QA_IMAGE_REGISTRY, by default the profile account's ECR in us-west-2.
set -euo pipefail

bucket=kikd-prod-sparkwing-qa-releases
ssm_key=/kikd-prod/sparkwing/qa-release-signing-key
region=us-west-2
export AWS_PROFILE="${AWS_PROFILE:-kikd-prod}"

if (( $# != 3 )); then
  echo "usage: bin/qa-release.sh <repo> <commit> <vX.Y.Z-qa.N>" >&2
  exit 2
fi
repo=$1 commit=$2 version=$3
if [[ ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-qa\.[1-9][0-9]*$ ]]; then
  echo "QA versions look like v0.68.0-qa.1, not $version" >&2
  exit 2
fi
sha=$(git -C "$repo" rev-parse --verify "$commit^{commit}")
# Checked before the build, which takes minutes, rather than at image time.
if ! docker buildx inspect --bootstrap 2>/dev/null | grep -q 'linux/arm64'; then
  echo "the docker buildx builder cannot build linux/arm64; register emulation with" >&2
  echo "  docker run --privileged --rm tonistiigi/binfmt --install arm64" >&2
  exit 1
fi
registry=${QA_IMAGE_REGISTRY:-$(aws sts get-caller-identity --query Account --output text).dkr.ecr.$region.amazonaws.com}
published=$(aws s3api list-objects-v2 --region "$region" --bucket "$bucket" --prefix "releases/download/$version/" --max-keys 1 --query KeyCount --output text)
if [[ "$published" != 0 ]]; then
  echo "$version is already published; QA releases are immutable, use the next -qa.N" >&2
  exit 1
fi

# Prints the object's ETag, or nothing when it is absent; any other error stops the release.
etag_of() {
  local out
  if out=$(aws s3api head-object --region "$region" --bucket "$bucket" --key "$1" --query ETag --output text 2>&1); then
    printf '%s' "$out"
  elif [[ "$out" != *"Not Found"* && "$out" != *"(404)"* ]]; then
    echo "$out" >&2
    return 1
  fi
}

# Writes only if the object still has the ETag read earlier (or is still absent),
# so a concurrent publication fails instead of dropping a version.
put_if_unchanged() {
  local key=$1 file=$2 etag=$3
  local condition=(--if-none-match '*')
  if [[ -n "$etag" ]]; then condition=(--if-match "$etag"); fi
  aws s3api put-object --region "$region" --bucket "$bucket" --key "$key" --body "$file" "${condition[@]}" >/dev/null
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
src=$work/src
mkdir -p "$work/previous"
# A clone rather than an archive: release builds stamp their version from git.
git clone --quiet --no-checkout "$repo" "$src"
git -C "$src" checkout --quiet --detach "$sha"
echo "==> building $version from $sha"

bash "$src/bin/build-web.sh"
# Passed per command, never exported, so no later build in this shell inherits it.
qa_flags=-tags=qatrust
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  env -C "$src" GOFLAGS="$qa_flags" GOOS="${target%/*}" GOARCH="${target#*/}" TAG="$version" bash bin/build-release-target.sh
done
dist=$src/dist
cp "$src/install/install.sh" "$dist/install.sh"
env -C "$dist" bash -c 'sha256sum sparkwing-* > SHA256SUMS'

# Pipelines fetch the Go SDK at this version from the QA proxy; see internal/engine/sdkinstall.
goproxy=$work/goproxy
module_dir=github.com/sparkwing-dev/sparkwing/@v
list_key=goproxy/$module_dir/list
GOWORK=off go -C "$src" run ./bin/qagoproxy --repo "$src" --revision "$sha" --version "$version" --out "$goproxy"
list_etag=$(etag_of "$list_key")
list=$work/list
: >"$list"
if [[ -n "$list_etag" ]]; then
  aws s3api get-object --region "$region" --bucket "$bucket" --key "$list_key" --if-match "$list_etag" "$list" >/dev/null
fi
echo "$version" >>"$list"

index_etag=$(etag_of versions.json)
previous=
if [[ -n "$index_etag" ]]; then
  aws s3api get-object --region "$region" --bucket "$bucket" --key versions.json --if-match "$index_etag" "$work/previous/versions.json" >/dev/null
  aws s3 cp --region "$region" "s3://$bucket/versions.json.sig" "$work/previous/versions.json.sig" --only-show-errors
  previous=$work/previous
fi

seed=$(aws ssm get-parameter --region "$region" --name "$ssm_key" --with-decryption --query Parameter.Value --output text)
SPARKWING_RELEASE_SIGNING_KEY=$seed GOFLAGS=$qa_flags GOWORK=off go -C "$src" run ./cmd/verify-release --dist "$dist"
SPARKWING_RELEASE_SIGNING_KEY=$seed GOFLAGS=$qa_flags GOWORK=off go -C "$src" run ./bin/qaindex --version "$version" --previous "$previous" --out "$work"
unset seed
GOFLAGS=$qa_flags GOWORK=off go -C "$src" run ./cmd/verify-release --dist "$dist" --verify

# Images first: a failed push publishes nothing clients read, and the version is
# then spent either way, since the registry's tags are immutable too.
aws ecr get-login-password --region "$region" | docker login --username AWS --password-stdin "$registry" >/dev/null
bash "$(dirname "${BASH_SOURCE[0]}")/publish-qa-images.sh" "$src" "$registry" "$version" "$sha"

aws s3 cp --region "$region" "$dist/" "s3://$bucket/releases/download/$version/" --recursive --only-show-errors
aws s3 cp --region "$region" "$goproxy/" "s3://$bucket/goproxy/" --recursive --only-show-errors
put_if_unchanged "$list_key" "$list" "$list_etag"
# The conditional body write claims the index; clients that read the new body
# before its signature lands fall back to their cached index until it does.
put_if_unchanged versions.json "$work/versions.json" "$index_etag"
aws s3 cp --region "$region" "$work/versions.json.sig" "s3://$bucket/versions.json.sig" --only-show-errors
echo "==> published $version ($sha)"
aws s3 ls --region "$region" "s3://$bucket/releases/download/$version/"
