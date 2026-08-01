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

changed=$'sparkwing/name with spaces\nand newline.go'
printf 'package fixture\n' > "$fixture/$changed"
export BASE_REF=HEAD
if /bin/bash "$fixture/bin/check-changelog.sh" >/dev/null 2>&1; then
  echo "check-changelog-test: covered filename did not require a changelog entry" >&2
  exit 1
fi

printf '\n- Added fixture.\n' >> "$fixture/CHANGELOG.md"
/bin/bash "$fixture/bin/check-changelog.sh"
