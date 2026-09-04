#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
spec="$REPO_ROOT/api/openapi.yaml"

rewritten="$(mktemp)"
trap 'rm -f "$rewritten"' EXIT

if ! go -C "$REPO_ROOT" run ./internal/apispec "$REPO_ROOT" "$spec" > "$rewritten"; then
  echo "check-api-spec: api/openapi.yaml disagrees with the controller's route table" >&2
  echo "or with the Go types its schemas name. The failure above says which." >&2
  exit 1
fi

if diff -u "$spec" "$rewritten"; then
  echo "check-api-spec: api/openapi.yaml documents every registered route and no other,"
  echo "carries each route's scope from the route table, and every schema's members match"
  echo "the JSON tags of the Go type it names in x-sparkwing-go-type"
  exit 0
fi

echo "" >&2
echo "check-api-spec: api/openapi.yaml is stale. Run \`bash bin/gen-api-docs.sh\`," >&2
echo "then describe any route it seeded as \"Registered route awaiting a description\"." >&2
exit 1
