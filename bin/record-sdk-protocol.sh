#!/usr/bin/env bash
set -euo pipefail

root=$(git rev-parse --show-toplevel)
version=${1:?usage: bash bin/record-sdk-protocol.sh PROTOCOL_VERSION}
if [[ -e "$root/internal/nodeproto/conformance/testdata/recordings/v$version" ]]; then
  echo "recordings for protocol $version already exist; published fixtures are immutable" >&2
  exit 1
fi
export GOWORK=off GOFLAGS=-trimpath
npm --prefix "$root/sdk/typescript" ci --ignore-scripts
go -C "$root" test -p 2 ./internal/nodeproto/conformance ./sdk/typescript/test/nativehost \
  -run 'TestGoSDKConformsV2|TestNativeConformance' -count=1 -args -record-protocol="$version"
