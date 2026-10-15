#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
spec="$REPO_ROOT/api/openapi.yaml"

rewritten="$(mktemp)"
trap 'rm -f "$rewritten"' EXIT

if ! go -C "$REPO_ROOT" run ./internal/apispec > "$rewritten"; then
  echo "check-api-spec: policy or shared wire generation failed" >&2
  exit 1
fi

if diff -u "$spec" "$rewritten"; then
  echo "check-api-spec: policy inventory and shared JSON shapes are current"
  exit 0
fi

echo "" >&2
echo "check-api-spec: api/openapi.yaml is stale. Run \`bash bin/gen-api-docs.sh\`," >&2
echo "then commit the generated document." >&2
exit 1
