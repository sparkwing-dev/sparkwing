#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

bash bin/gen-cli-docs.sh
bash bin/gen-config-docs.sh
bash bin/gen-sdk-docs.sh
bash bin/gen-api-docs.sh
bash bin/regen-api-snapshot.sh

# helm packs source file mtimes into the tarball, so re-vendoring a
# chart that already matches its source rewrites bytes that carry no change.
if go test ./charts -run TestVendoredRunnerBundleMatchesItsSource >/dev/null 2>&1; then
  echo "runner-bundle tarball matches charts/sparkwing-runner-bundle; not re-vendored"
elif command -v helm >/dev/null 2>&1; then
  helm dependency update ./charts/sparkwing-full
else
  echo "regen-all: the vendored runner bundle is stale and helm is not installed" >&2
  exit 1
fi

bash bin/sync-docs.sh >/dev/null

echo "regen-all: cli/config/sdk/api docs, api/openapi.yaml, .apidiff, chart vendor, pkg/docs mirror"
