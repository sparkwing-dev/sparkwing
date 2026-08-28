#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

out=${1:-.apidiff}
mkdir -p "$out"
go run ./cmd/apidiff "$out"
echo "Wrote API snapshots to $out/"
