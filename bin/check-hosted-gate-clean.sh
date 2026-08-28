#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <expected-head>" >&2
  exit 2
fi

expected_head="$1"
actual_head="$(git rev-parse HEAD)"
if [ "$actual_head" != "$expected_head" ]; then
  echo "hosted gate changed HEAD: got $actual_head, want $expected_head" >&2
  exit 1
fi

status="$(git status --porcelain --untracked-files=all)"
if [ -n "$status" ]; then
  echo "hosted gate changed the checked-out tree or index:" >&2
  printf '%s\n' "$status" >&2
  exit 1
fi
