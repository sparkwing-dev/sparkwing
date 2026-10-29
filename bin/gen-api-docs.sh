#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
case "${1:-}" in
  "" | --check) ;;
  *)
    echo "usage: bash bin/gen-api-docs.sh [--check]" >&2
    exit 2
    ;;
esac

if [ "${1:-}" = --check ]; then
  generated="$(mktemp)"
  trap 'rm -f "$generated"' EXIT
  go -C "$REPO_ROOT" run ./internal/apiref > "$generated"
  if ! diff -u "$REPO_ROOT/docs/api-reference.md" "$generated"; then
    echo "gen-api-docs: docs/api-reference.md is stale; run bash bin/gen-api-docs.sh" >&2
    exit 1
  fi
  exec bash "$REPO_ROOT/bin/check-api-spec.sh"
fi

go -C "$REPO_ROOT" run ./internal/apiref > "$REPO_ROOT/docs/api-reference.md"

spec="$REPO_ROOT/api/openapi.yaml"
rewritten="$(mktemp)"
trap 'rm -f "$rewritten"' EXIT
go -C "$REPO_ROOT" run ./internal/apispec > "$rewritten"
mv "$rewritten" "$spec"

bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/api-reference.md + api/openapi.yaml + synced pkg/docs/mirror"
