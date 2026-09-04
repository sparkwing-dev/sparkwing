#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
spec="$REPO_ROOT/api/openapi.yaml"

rewritten="$(mktemp)"
trap 'rm -f "$rewritten"' EXIT

if ! go -C "$REPO_ROOT" run ./internal/apispec "$REPO_ROOT" "$spec" > "$rewritten"; then
  echo "check-api-spec: api/openapi.yaml disagrees with the controller's route table." >&2
  exit 1
fi

if diff -u "$spec" "$rewritten"; then
  echo "check-api-spec: api/openapi.yaml matches the route table"
  exit 0
fi

echo "" >&2
echo "check-api-spec: api/openapi.yaml is stale. Run \`bash bin/gen-api-docs.sh\`," >&2
echo "then describe any route it seeded as \"Registered route awaiting a description\"." >&2
exit 1
