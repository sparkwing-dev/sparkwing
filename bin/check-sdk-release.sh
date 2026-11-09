#!/usr/bin/env bash
set -euo pipefail
repo=$(git rev-parse --show-toplevel)
export GOFLAGS=-trimpath
export GOWORK=off
go -C "$repo/sparkwing" test -p 1 ./...
npm --prefix "$repo/sdk/typescript" ci --ignore-scripts --no-audit --no-fund
# The host must test the unpublished SDK source rather than its last public tag.
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
cp "$repo/go.mod" "$scratch/go.mod"
cp "$repo/go.sum" "$scratch/go.sum"
go -C "$repo" mod edit -modfile "$scratch/go.mod" -replace "github.com/sparkwing-dev/sparkwing/sparkwing=$repo/sparkwing"
export GOFLAGS="-trimpath -modfile=$scratch/go.mod"
go -C "$repo" test -p 1 ./internal/nodeproto/conformance
go -C "$repo" test -p 1 ./sdk/typescript/test/nativehost -run TestNativeConformance
