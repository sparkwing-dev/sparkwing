#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <release-tag> <source-sha> <patch-file>" >&2
  exit 2
fi

tag="$1"
source_sha="$2"
patch_file="$3"

if [ "$tag" != "v0.37.2" ]; then
  echo "no test repair is registered for $tag"
  exit 0
fi

expected_patch_oid="511049535934f8311a26a9a6f63509a477951009"
actual_patch_oid="$(git hash-object "$patch_file")"
if [ "$actual_patch_oid" != "$expected_patch_oid" ]; then
  echo "v0.37.2 test repair checksum mismatch" >&2
  exit 1
fi

test "$source_sha" = "678d2b50fd650a3793edbe9b6964aa1a1ba1f81b"
test "$(git hash-object internal/orchestrator/repo_ident_test.go)" = "e5683d304796b05aff49ee78b3232eaa902770e4"
git apply --unidiff-zero --check "$patch_file"
git apply --unidiff-zero "$patch_file"
test "$(git hash-object internal/orchestrator/repo_ident_test.go)" = "3062227c1f3cafc676dc9363ebefb5644f09989d"
echo "applied the audited v0.37.2 test-only repair"
