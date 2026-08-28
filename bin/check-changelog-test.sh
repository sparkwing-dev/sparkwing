#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/sparkwing-changelog-test.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT

mkdir -p "$fixture/bin" "$fixture/sparkwing"
cp "$root/bin/check-changelog.sh" "$fixture/bin/check-changelog.sh"
printf '# Changelog\n\n## [Unreleased]\n' > "$fixture/CHANGELOG.md"
git -C "$fixture" init -q
git -C "$fixture" config user.email test@example.com
git -C "$fixture" config user.name test
git -C "$fixture" config commit.gpgsign false
git -C "$fixture" add .
git -C "$fixture" commit -qm baseline
export BASE_REF=HEAD
if ! /bin/bash "$fixture/bin/check-changelog.sh"; then
  echo "check-changelog-test: unchanged fixture failed" >&2
  exit 1
fi

changed=$'sparkwing/name with spaces\nand newline.go'
printf 'package fixture\n' > "$fixture/$changed"
git -C "$fixture" add -- "$changed"
if git -C "$fixture" diff --cached --quiet -- "$changed"; then
  echo "check-changelog-test: covered fixture was not staged" >&2
  exit 1
fi
set +e
output="$(/bin/bash "$fixture/bin/check-changelog.sh" 2>&1)"
status=$?
set -e
if [[ $status -ne 1 ]]; then
  echo "check-changelog-test: covered filename exited $status, want 1" >&2
  exit 1
fi
if [[ "$output" != *"check-changelog: CHANGELOG.md update required"* ]] ||
   [[ "$output" != *"$changed"* ]]; then
  echo "check-changelog-test: covered filename did not produce the policy diagnostic" >&2
  printf '%s\n' "$output" >&2
  exit 1
fi

printf '\n- Added fixture.\n' >> "$fixture/CHANGELOG.md"
/bin/bash "$fixture/bin/check-changelog.sh"
