#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
usage: check-release-schema-parity.sh --asset <binary> [--reference <binary>] [--repo <dir>]

Asserts a built release asset embeds the same runs-store schema version as the
tagged source compiles. A version string must imply identical code across both
install paths, the GitHub-Release binary and `go install ...@tag`, so an asset
built from a different commit than the tag can ship a schema the tag never had.

  --asset      the release binary whose embedded schema is verified. Required.
  --reference  a binary independently compiled from the tagged tree. When
               omitted, one is built from --repo via `go build ./cmd/sparkwing`.
  --repo       the checkout the reference is built from. Defaults to the
               repository this script lives in.
EOF
}

die() {
  echo "schema-parity: $*" >&2
  exit 1
}

# safety: an argument this script cannot act on leaves it with nothing to
# verify, and a check that verified nothing must not answer like one that
# passed. Exit 2 separates that from a parity failure at 1.
usage_die() {
  echo "schema-parity: $*" >&2
  usage >&2
  exit 2
}

ASSET=""
REFERENCE=""
REPO=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --asset) [[ $# -ge 2 ]] || usage_die "--asset needs a value"; ASSET="$2"; shift 2 ;;
    --reference) [[ $# -ge 2 ]] || usage_die "--reference needs a value"; REFERENCE="$2"; shift 2 ;;
    --repo) [[ $# -ge 2 ]] || usage_die "--repo needs a value"; REPO="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage_die "unknown flag: $1" ;;
  esac
done

command -v jq >/dev/null 2>&1 || die "jq is required"
[[ -n "$ASSET" ]] || usage_die "--asset is required"
[[ -x "$ASSET" ]] || die "asset is not an executable file: $ASSET"

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
