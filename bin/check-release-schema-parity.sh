#!/usr/bin/env bash
# Pre-publish gate: assert a built release asset embeds the same
# runs-store schema version as the tagged source compiles.
#
# A version string must imply identical code across both install
# paths: the GitHub-Release binary and `go install ...@tag`. If the
# asset was built from a different commit than the tag, its embedded
# schema can drift from what the tag compiles -- the failure mode
# behind the v0.9.0 schema-skew incident. This check rebuilds the
# schema reference straight from the tagged tree and refuses the
# release when the asset disagrees.
#
# Usage:
#   bin/check-release-schema-parity.sh --asset dist/sparkwing-linux-amd64
#   bin/check-release-schema-parity.sh --asset <bin> --reference <bin>
#   bin/check-release-schema-parity.sh --asset <bin> --repo /path/to/checkout
#
# --asset      the release binary whose embedded schema is verified.
# --reference  a binary independently compiled from the tagged tree;
#              when omitted, one is built from --repo (default: the
#              repo this script lives in) via `go build ./cmd/sparkwing`.
set -euo pipefail

die() {
  echo "schema-parity: $*" >&2
  exit 1
}

ASSET=""
REFERENCE=""
REPO=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --asset) ASSET="${2:-}"; shift 2 ;;
    --reference) REFERENCE="${2:-}"; shift 2 ;;
    --repo) REPO="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) die "unknown flag: $1 (see --help)" ;;
  esac
done

command -v jq >/dev/null 2>&1 || die "jq is required"
[[ -n "$ASSET" ]] || die "--asset is required"
[[ -x "$ASSET" ]] || die "asset is not an executable file: $ASSET"

# Pull the embedded schema version out of a built binary. --offline
# skips the latest-release network probe; the schema field is local.
embedded_schema() {
  local bin="$1"
  local out
  if ! out="$("$bin" version -o json --offline 2>/dev/null)"; then
    die "could not run '$bin version'"
  fi
  local v
  v="$(printf '%s' "$out" | jq -r '.schema_version')"
  case "$v" in
    ''|null) die "binary $bin reports no schema_version (too old to verify)" ;;
    *[!0-9]*) die "binary $bin reports non-numeric schema_version: $v" ;;
  esac
  printf '%s' "$v"
}

TMP=""
cleanup() { [[ -n "$TMP" ]] && rm -rf "$TMP"; }
trap cleanup EXIT

if [[ -z "$REFERENCE" ]]; then
  if [[ -z "$REPO" ]]; then
    REPO="$(cd "$(dirname "$0")/.." && pwd)"
  fi
  [[ -d "$REPO/cmd/sparkwing" ]] || die "no cmd/sparkwing under --repo $REPO"
  TMP="$(mktemp -d)"
  REFERENCE="$TMP/sparkwing-reference"
  echo "schema-parity: compiling schema reference from $REPO"
  CGO_ENABLED=0 go -C "$REPO" build -o "$REFERENCE" ./cmd/sparkwing \
    || die "reference build from $REPO failed"
fi
[[ -x "$REFERENCE" ]] || die "reference is not an executable file: $REFERENCE"

ASSET_SCHEMA="$(embedded_schema "$ASSET")"
REF_SCHEMA="$(embedded_schema "$REFERENCE")"

echo "schema-parity: asset=$ASSET_SCHEMA reference=$REF_SCHEMA"
if [[ "$ASSET_SCHEMA" != "$REF_SCHEMA" ]]; then
  die "asset embeds schema $ASSET_SCHEMA but the tagged source compiles schema $REF_SCHEMA. The asset was not built from the tagged commit; refuse to publish a version that ships two different schemas."
fi

echo "schema-parity: OK (both embed schema $ASSET_SCHEMA)"
