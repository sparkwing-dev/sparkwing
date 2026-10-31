#!/usr/bin/env bash
set -euo pipefail

# The website and the API use different buckets. Only these two objects belong
# to release publication.
bucket=kikd-prod-sparkwing-website
distribution=E3V178ZREDVPYN
root="$(realpath "$(dirname "${BASH_SOURCE[0]}")/..")"
: "${TAG:?TAG must name an existing public stable release}"
[[ "$TAG" =~ ^v0\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
out="${RUNNER_TEMP:-$work}/release-index"
mkdir -p "$work/previous" "$out" "$work/check"
aws s3api get-object --bucket "$bucket" --key versions.json "$work/previous/versions.json" >/dev/null
signature_count="$(aws s3api list-objects-v2 --bucket "$bucket" --prefix versions.json \
  --query 'length(Contents[?Key==`versions.json.sig`] || `[]`)' --output text)"
case "$signature_count" in
  1) aws s3api get-object --bucket "$bucket" --key versions.json.sig "$work/previous/versions.json.sig" >/dev/null ;;
  0) [[ "${BOOTSTRAP_INDEX:-false}" == true ]] || { echo 'Current index has no signature; use the documented one-time bootstrap' >&2; exit 1; } ;;
  *) echo 'Unexpected signature inventory' >&2; exit 1 ;;
esac
args=(--index-out "$out" --previous-index "$work/previous" --require-version "$TAG")
if [[ "${BOOTSTRAP_INDEX:-false}" == true ]]; then args+=(--bootstrap-index); fi
GOWORK=off go -C "$root" run ./cmd/verify-release "${args[@]}"
for object in versions.json versions.json.sig; do
  content_type=application/json
  if [[ "$object" == *.sig ]]; then content_type=application/octet-stream; fi
  aws s3api put-object --bucket "$bucket" --key "$object" --body "$out/$object" \
    --content-type "$content_type" --cache-control 'public, max-age=60, must-revalidate' >/dev/null
  aws s3api get-object --bucket "$bucket" --key "$object" "$work/check/$object" >/dev/null
  cmp "$out/$object" "$work/check/$object"
done
invalidation="$(aws cloudfront create-invalidation --distribution-id "$distribution" \
  --paths /versions.json /versions.json.sig --query Invalidation.Id --output text)"
aws cloudfront wait invalidation-completed --distribution-id "$distribution" --id "$invalidation"
for object in versions.json versions.json.sig; do
  curl --fail --silent --show-error "https://sparkwing.dev/$object" -o "$work/check/$object"
  cmp "$out/$object" "$work/check/$object"
done
echo "Published and read back the signed stable index for $TAG"
