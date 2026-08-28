#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/bin/check-hosted-gate-clean.sh"
TEST_ROOT="$(mktemp -d)"
CASE_ROOT="$TEST_ROOT/repo"
OUTPUT="$TEST_ROOT/output"
trap 'rm -rf "$TEST_ROOT"' EXIT

mkdir "$CASE_ROOT"
git -C "$CASE_ROOT" init -q
git -C "$CASE_ROOT" config user.email hosted-gate-test@example.com
git -C "$CASE_ROOT" config user.name hosted-gate-test
git -C "$CASE_ROOT" config commit.gpgsign false
printf 'clean\n' >"$CASE_ROOT/tracked.txt"
git -C "$CASE_ROOT" add tracked.txt
git -C "$CASE_ROOT" commit -qm initial
target="$(git -C "$CASE_ROOT" rev-parse HEAD)"

run_check() {
  (
    cd "$CASE_ROOT"
    bash "$CHECK" "$target"
  )
}

expect_failure() {
  label="$1"
  diagnostic="$2"
  if run_check >"$OUTPUT" 2>&1; then
    echo "check-hosted-gate-clean-test: $label mutation passed" >&2
    exit 1
  fi
  if ! grep -Fq "$diagnostic" "$OUTPUT"; then
    echo "check-hosted-gate-clean-test: $label mutation failed for the wrong reason" >&2
    cat "$OUTPUT" >&2
    exit 1
  fi
}

expect_status() {
  label="$1"
  expected="$2"
  actual="$(git -C "$CASE_ROOT" status --porcelain --untracked-files=all)"
  if [ "$actual" != "$expected" ]; then
    echo "check-hosted-gate-clean-test: unexpected $label fixture state: $actual" >&2
    exit 1
  fi
}

run_check

printf 'unstaged\n' >>"$CASE_ROOT/tracked.txt"
expect_status unstaged ' M tracked.txt'
expect_failure unstaged ' M tracked.txt'
git -C "$CASE_ROOT" restore tracked.txt

printf 'staged\n' >>"$CASE_ROOT/tracked.txt"
git -C "$CASE_ROOT" add tracked.txt
expect_status staged 'M  tracked.txt'
expect_failure staged 'M  tracked.txt'
git -C "$CASE_ROOT" restore --staged --worktree tracked.txt

printf 'untracked\n' >"$CASE_ROOT/untracked.txt"
expect_status untracked '?? untracked.txt'
expect_failure untracked '?? untracked.txt'
rm "$CASE_ROOT/untracked.txt"

git -C "$CASE_ROOT" commit --allow-empty -qm mutation
expect_status committed ''
expect_failure committed 'hosted gate changed HEAD'

echo "check-hosted-gate-clean-test: ok"
