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

git -C "$CASE_ROOT" reset --hard -q "$target"
mkdir -p "$CASE_ROOT/testdata/kind-e2e/repo/.sparkwing" "$CASE_ROOT/pkg/scaffold" "$CASE_ROOT/.apidiff" "$CASE_ROOT/.sparkwing"
cat >"$CASE_ROOT/testdata/kind-e2e/repo/.sparkwing/go.mod" <<'EOF'
module release-fixture

go 1.26.0

require github.com/sparkwing-dev/sparkwing v0.37.1
EOF
printf 'github.com/sparkwing-dev/sparkwing v0.37.1 h1:old\n' >"$CASE_ROOT/testdata/kind-e2e/repo/.sparkwing/go.sum"
printf 'module release-pipeline\n\ngo 1.26.0\n\nrequire github.com/sparkwing-dev/sparkwing v0.37.1\n\nreplace github.com/sparkwing-dev/sparkwing => ..\n' >"$CASE_ROOT/.sparkwing/go.mod"
printf 'github.com/sparkwing-dev/sparkwing v0.37.1 h1:pipeline-old\n' >"$CASE_ROOT/.sparkwing/go.sum"
printf 'package scaffold\n\nconst FallbackSDKVersion = "v0.37.1"\n' >"$CASE_ROOT/pkg/scaffold/version.go"
printf '# pkg/scaffold\n\nconst FallbackSDKVersion = "v0.37.1"\n' >"$CASE_ROOT/.apidiff/pkg_scaffold.txt"
git -C "$CASE_ROOT" add testdata pkg/scaffold/version.go .apidiff/pkg_scaffold.txt .sparkwing/go.mod .sparkwing/go.sum
git -C "$CASE_ROOT" commit -qm fixture
target="$(git -C "$CASE_ROOT" rev-parse HEAD)"

sed -i.bak 's/v0.37.1/v0.37.2/' "$CASE_ROOT/testdata/kind-e2e/repo/.sparkwing/go.mod"
rm "$CASE_ROOT/testdata/kind-e2e/repo/.sparkwing/go.mod.bak"
sed -i.bak 's/v0.37.1/v0.37.2/' "$CASE_ROOT/testdata/kind-e2e/repo/.sparkwing/go.sum"
rm "$CASE_ROOT/testdata/kind-e2e/repo/.sparkwing/go.sum.bak"
sed -i.bak 's/v0.37.1/v0.37.2/' "$CASE_ROOT/pkg/scaffold/version.go"
rm "$CASE_ROOT/pkg/scaffold/version.go.bak"
sed -i.bak 's/v0.37.1/v0.37.2/' "$CASE_ROOT/.apidiff/pkg_scaffold.txt"
rm "$CASE_ROOT/.apidiff/pkg_scaffold.txt.bak"
sed -i.bak 's/v0.37.1/v0.37.2/; /replace github.com\/sparkwing-dev\/sparkwing => ../d' "$CASE_ROOT/.sparkwing/go.mod"
rm "$CASE_ROOT/.sparkwing/go.mod.bak"
sed -i.bak 's/v0.37.1/v0.37.2/' "$CASE_ROOT/.sparkwing/go.sum"
rm "$CASE_ROOT/.sparkwing/go.sum.bak"
self_pin_oid="$(git -C "$CASE_ROOT" diff --binary -- .apidiff/pkg_scaffold.txt .sparkwing/go.mod .sparkwing/go.sum pkg/scaffold/version.go testdata/kind-e2e/repo/.sparkwing/go.mod testdata/kind-e2e/repo/.sparkwing/go.sum | git hash-object --stdin)"
(
  cd "$CASE_ROOT"
  bash "$CHECK" --release-self-pin v0.37.2 "$self_pin_oid" "$target"
)

printf 'unrelated\n' >>"$CASE_ROOT/tracked.txt"
if (
  cd "$CASE_ROOT"
  bash "$CHECK" --release-self-pin v0.37.2 "$self_pin_oid" "$target"
) >"$OUTPUT" 2>&1; then
  echo 'check-hosted-gate-clean-test: release allowance admitted an unrelated edit' >&2
  exit 1
fi
grep -Fq 'hosted gate changed files outside the release self-pin allowance' "$OUTPUT"

git -C "$CASE_ROOT" restore tracked.txt
if (
  cd "$CASE_ROOT"
  bash "$CHECK" --release-self-pin v0.37.3 "$self_pin_oid" "$target"
) >"$OUTPUT" 2>&1; then
  echo 'check-hosted-gate-clean-test: release allowance admitted the wrong version' >&2
  exit 1
fi
grep -Fq 'release fixture does not pin v0.37.3' "$OUTPUT"

sed -i.bak 's/h1:old/h1:tampered/' "$CASE_ROOT/testdata/kind-e2e/repo/.sparkwing/go.sum"
rm "$CASE_ROOT/testdata/kind-e2e/repo/.sparkwing/go.sum.bak"
if (
  cd "$CASE_ROOT"
  bash "$CHECK" --release-self-pin v0.37.2 "$self_pin_oid" "$target"
) >"$OUTPUT" 2>&1; then
  echo 'check-hosted-gate-clean-test: release allowance admitted a changed self-pin patch' >&2
  exit 1
fi
grep -Fq 'release self-pin patch changed during the hosted gate' "$OUTPUT"

echo "check-hosted-gate-clean-test: ok"
