#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
case "${1:-}" in
  "" | --check) ;;
  *)
    echo "usage: bash bin/gen-sdk-docs.sh [--check]" >&2
    exit 2
    ;;
esac

generate() {
  go -C "$REPO_ROOT" run ./internal/sdkref "$REPO_ROOT" "$1" >/dev/null
}

if [ "${1:-}" = --check ]; then
  generated="$(mktemp -d)"
  trap 'rm -rf "$generated"' EXIT
  generate "$generated"
  bash "$REPO_ROOT/bin/check-generated-pages.sh" "$generated" "sdk-*.md" \
    "GENERATED from the .sparkwing. package via go/doc" "bash bin/gen-sdk-docs.sh"
  exit 0
fi

generate "$REPO_ROOT/docs"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/sdk-reference.md + docs/sdk-<name>.md + synced pkg/docs/mirror"
