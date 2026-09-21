#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHECK="$ROOT/bin/check-release-schema-parity.sh"
CASE_ROOT="$(mktemp -d)"
trap 'rm -rf "$CASE_ROOT"' EXIT

expect_status() {
  local want="$1" label="$2"
  shift 2
  local status=0
  "$@" >"$CASE_ROOT/out" 2>&1 || status=$?
  if [ "$status" -ne "$want" ]; then
    echo "schema-parity test: $label exited $status, want $want" >&2
    cat "$CASE_ROOT/out" >&2
    exit 1
  fi
}

expect_output() {
  local needle="$1" label="$2"
  if ! grep -Fq -e "$needle" "$CASE_ROOT/out"; then
    echo "schema-parity test: $label does not say \"$needle\"" >&2
    cat "$CASE_ROOT/out" >&2
    exit 1
  fi
}

expect_status 2 "no arguments" bash "$CHECK"
expect_output "--asset is required" "no arguments"

expect_status 2 "dangling --asset" bash "$CHECK" --asset
expect_output "--asset needs a value" "dangling --asset"

expect_status 2 "empty --asset" bash "$CHECK" --asset ""
expect_output "--asset is required" "empty --asset"

expect_status 2 "unknown flag" bash "$CHECK" --nope
expect_output "unknown flag" "unknown flag"

expect_status 1 "missing asset file" bash "$CHECK" --asset "$CASE_ROOT/absent"
expect_output "asset is not an executable file" "missing asset file"

# The help text once came from a line range of this script's own header. A
# refactor took the header and left the range printing shell source under a
# zero exit, which reads like a check that ran.
expect_status 0 "--help" bash "$CHECK" --help
expect_output "usage: check-release-schema-parity.sh" "--help"
if grep -q 'set -euo pipefail' "$CASE_ROOT/out"; then
  echo "schema-parity test: --help prints the script's source instead of its usage" >&2
  exit 1
fi
