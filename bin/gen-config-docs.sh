#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
case "${1:-}" in
  "" | --check) ;;
  *)
    echo "usage: bash bin/gen-config-docs.sh [--check]" >&2
    exit 2
    ;;
esac

if [ "${1:-}" = --check ]; then
  generated="$(mktemp)"
  trap 'rm -f "$generated"' EXIT
  go -C "$REPO_ROOT" run ./internal/configref "$REPO_ROOT" > "$generated"
  if ! diff -u "$REPO_ROOT/docs/config-reference.md" "$generated"; then
    echo "gen-config-docs: docs/config-reference.md is stale; run bash bin/gen-config-docs.sh" >&2
    exit 1
  fi
  exit 0
fi

go -C "$REPO_ROOT" run ./internal/configref "$REPO_ROOT" > "$REPO_ROOT/docs/config-reference.md"
bash "$REPO_ROOT/bin/sync-docs.sh" >/dev/null

echo "generated docs/config-reference.md + synced pkg/docs/mirror"
