#!/usr/bin/env bash
set -euo pipefail

release_tag=""
if [ "${1:-}" = "--release-self-pin" ]; then
  if [ "$#" -ne 4 ]; then
    echo "usage: $0 [--release-self-pin <tag> <expected-patch-oid>] <expected-head>" >&2
    exit 2
  fi
  release_tag="$2"
  expected_patch_oid="$3"
  shift 3
fi
if [ "$#" -ne 1 ]; then
  echo "usage: $0 [--release-self-pin <tag> <expected-patch-oid>] <expected-head>" >&2
  exit 2
fi

expected_head="$1"
actual_head="$(git rev-parse HEAD)"
if [ "$actual_head" != "$expected_head" ]; then
  echo "hosted gate changed HEAD: got $actual_head, want $expected_head" >&2
  exit 1
fi

status="$(git status --porcelain --untracked-files=all)"
if [ -z "$release_tag" ] && [ -n "$status" ]; then
  echo "hosted gate changed the checked-out tree or index:" >&2
  printf '%s\n' "$status" >&2
  exit 1
fi
if [ -n "$release_tag" ]; then
  if ! [[ "$release_tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]]; then
    echo "invalid release self-pin tag: $release_tag" >&2
    exit 2
  fi
  mod="testdata/kind-e2e/repo/.sparkwing/go.mod"
  sum="testdata/kind-e2e/repo/.sparkwing/go.sum"
  fallback="pkg/scaffold/version.go"
  snapshot=".apidiff/pkg_scaffold.txt"
  pipeline_mod=".sparkwing/go.mod"
  pipeline_sum=".sparkwing/go.sum"
  expected_status="$(printf ' M %s\n M %s\n M %s\n M %s\n M %s\n M %s' "$snapshot" "$pipeline_mod" "$pipeline_sum" "$fallback" "$mod" "$sum")"
  if [ "$status" != "$expected_status" ]; then
    echo "hosted gate changed files outside the release self-pin allowance:" >&2
    printf '%s\n' "$status" >&2
    exit 1
  fi
  if ! grep -Eq "^require github.com/sparkwing-dev/sparkwing ${release_tag//./\\.}$" "$mod"; then
    echo "release fixture does not pin $release_tag" >&2
    exit 1
  fi
  if ! grep -Fxq "const FallbackSDKVersion = \"$release_tag\"" "$fallback"; then
    echo "release scaffold fallback does not pin $release_tag" >&2
    exit 1
  fi
  if ! grep -Fxq "const FallbackSDKVersion = \"$release_tag\"" "$snapshot"; then
    echo "release scaffold snapshot does not pin $release_tag" >&2
    exit 1
  fi
  if ! grep -Eq "^[[:space:]]*(require[[:space:]]+)?github.com/sparkwing-dev/sparkwing ${release_tag//./\\.}([[:space:]]|$)" "$pipeline_mod"; then
    echo "release pipeline does not pin $release_tag" >&2
    exit 1
  fi
  if grep -Eq '^[[:space:]]*(replace[[:space:]]+)?github.com/sparkwing-dev/sparkwing([[:space:]]|$).*=>' "$pipeline_mod"; then
    echo "release pipeline retains the local sparkwing self-replace" >&2
    exit 1
  fi
  actual_patch_oid="$(git diff --binary -- "$snapshot" "$pipeline_mod" "$pipeline_sum" "$fallback" "$mod" "$sum" | git hash-object --stdin)"
  if [ "$actual_patch_oid" != "$expected_patch_oid" ]; then
    echo "release self-pin patch changed during the hosted gate" >&2
    exit 1
  fi
fi
