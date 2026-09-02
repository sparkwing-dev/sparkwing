#!/usr/bin/env bash
set -euo pipefail

# Moves each release image's version tag onto the digest that was scanned and
# signed, then writes the image-digests.json asset. A published version tag is
# immutable: an operator who pinned it audited those bytes, so the move needs
# either an absent tag or an explicit override.

TAG="${TAG:?TAG is required}"
FORCE_RETAG="${FORCE_RETAG:-false}"
DIGEST_DIR="${DIGEST_DIR:-scanned-image-digests}"
OUTPUT="${OUTPUT:-image-digests/image-digests.json}"
IMAGE_PREFIX="${IMAGE_PREFIX:-ghcr.io/sparkwing-dev}"

binaries=(
  sparkwing-controller
  sparkwing-runner
  sparkwing-cache
  sparkwing-logs
  sparkwing-web
)

work="$(mktemp -d "${TMPDIR:-/tmp}/publish-image-tags.XXXXXX")"
trap 'rm -rf "$work"' EXIT

# Prints the digest the tag resolves to, prints nothing when the tag does not
# exist, and fails on any other registry answer so a lookup outage cannot read
# as an absent tag.
resolve_published() {
  local ref="$1"
  local out
  if out="$(docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$ref" 2>"$work/inspect.err")"; then
    printf '%s' "$out" | tr -d '\r\n '
    return 0
  fi
  if grep -qiE 'not found|manifest unknown|manifest_unknown|name unknown|name_unknown' "$work/inspect.err"; then
    return 0
  fi
  echo "resolving ${ref} failed; refusing to retag on an unreadable registry answer" >&2
  cat "$work/inspect.err" >&2
  return 1
}

digests=()
refusals=()

# Every tag is inspected and compared before any tag moves, so a refusal cannot
# leave the registry half-moved.
for binary in "${binaries[@]}"; do
  digest="$(tr -d '\r\n' <"$DIGEST_DIR/$binary")"
  if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "scanned digest for ${binary} is not a sha256 digest: ${digest}" >&2
    exit 1
  fi
  digests+=("$digest")

  image="${IMAGE_PREFIX}/${binary}"
  published="$(resolve_published "${image}:${TAG}")"
  if [ -n "$published" ] && [ "$published" != "$digest" ]; then
    if [ "$FORCE_RETAG" != "true" ]; then
      refusals+=("${image}:${TAG} already points at ${published}; refusing to move it to ${digest}")
    else
      echo "::warning::force_retag moves ${image}:${TAG} from ${published} to ${digest}"
    fi
  fi
done

if [ "${#refusals[@]}" -gt 0 ]; then
  printf '%s\n' "${refusals[@]}" >&2
  echo "no image tag was moved (rerun with force_retag to override)" >&2
  exit 1
fi

for i in "${!binaries[@]}"; do
  image="${IMAGE_PREFIX}/${binaries[$i]}"
  tag_args=(--tag "${image}:${TAG}")
  if [[ "$TAG" != *-* ]]; then
    tag_args+=(--tag "${image}:latest")
  fi
  docker buildx imagetools create \
    "${tag_args[@]}" \
    "${image}@${digests[$i]}"
done

# The asset names what the tag actually resolves to after the move, not what
# the job intended to publish.
entries=""
for i in "${!binaries[@]}"; do
  image="${IMAGE_PREFIX}/${binaries[$i]}"
  resolved="$(resolve_published "${image}:${TAG}")"
  if [ "$resolved" != "${digests[$i]}" ]; then
    echo "${image}:${TAG} resolves to ${resolved:-no manifest} after the retag; expected ${digests[$i]}" >&2
    exit 1
  fi
  entries="${entries:+${entries},}$(printf '{"image":"%s","tag":"%s","digest":"%s"}' "$image" "$TAG" "$resolved")"
done

mkdir -p "$(dirname "$OUTPUT")"
printf '{"tag":"%s","images":[%s]}\n' "$TAG" "$entries" |
  jq . >"$OUTPUT"
cat "$OUTPUT"
