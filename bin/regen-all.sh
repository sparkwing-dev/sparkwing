#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

bash bin/gen-cli-docs.sh
bash bin/gen-config-docs.sh
bash bin/gen-sdk-docs.sh
bash bin/gen-api-docs.sh
bash bin/regen-api-snapshot.sh
go test ./pkg/wingwire -run TestWireShapes -update >/dev/null

# helm packs source file mtimes into the tarball, so re-vendoring a
# chart that already matches its source rewrites bytes that carry no change.
# Only a test that ran and failed means the vendored copy is stale; a build
# error must not trigger a re-vendor.
vendor_check="$(go test ./charts -run TestVendoredRunnerBundleMatchesItsSource 2>&1)" && vendor_status=0 || vendor_status=$?
if [[ $vendor_status -eq 0 ]]; then
  echo "runner-bundle tarball matches charts/sparkwing-runner-bundle; not re-vendored"
elif ! grep -q -- '--- FAIL' <<<"$vendor_check"; then
  echo "regen-all: could not run the chart vendor test:" >&2
  echo "$vendor_check" >&2
  exit 1
elif command -v helm >/dev/null 2>&1; then
  helm dependency update ./charts/sparkwing-full
else
  echo "regen-all: the vendored runner bundle is stale and helm is not installed" >&2
  exit 1
fi

bash bin/sync-docs.sh >/dev/null

echo "regen-all: cli/config/sdk/api docs, api/openapi.yaml, .apidiff, wingwire shapes, chart vendor, pkg/docs mirror"
