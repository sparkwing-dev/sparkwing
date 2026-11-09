#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/test-git-env.sh"
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/sparkwing-changelog-test.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT

mkdir -p "$fixture/bin" "$fixture/sparkwing" "$fixture/docs" "$fixture/pkg/docs"
cp "$root/bin/check-changelog.sh" "$fixture/bin/check-changelog.sh"
cp "$root/bin/sync-docs.sh" "$fixture/bin/sync-docs.sh"
printf 'placeholder\n' > "$fixture/docs/index.md"
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

check_surface() {
  local changed="$1" want="$2" output status
  mkdir -p "$(dirname "$fixture/$changed")"
  printf 'fixture change\n' > "$fixture/$changed"
  git -C "$fixture" add -- "$changed"
  status=0
  output="$(/bin/bash "$fixture/bin/check-changelog.sh" 2>&1)" || status=$?
  if [[ "$status" -ne "$want" ]]; then
    printf 'check-changelog-test: %s exited %s, want %s\n%s\n' "$changed" "$status" "$want" "$output" >&2
    exit 1
  fi
  if [[ "$want" -eq 1 ]]; then
    if [[ "$output" != *"docs/changes/<slug>.md entry required"* || "$output" != *"$changed"* ]]; then
      printf 'check-changelog-test: missing covered-surface diagnostic for %s\n%s\n' "$changed" "$output" >&2
      exit 1
    fi
    mkdir -p "$fixture/docs/changes"
    printf '### Fixed\n\n- **fixture:** Changed default\n' > "$fixture/docs/changes/test.md"
    /bin/bash "$fixture/bin/check-changelog.sh"
    rm "$fixture/docs/changes/test.md"
  fi
  git -C "$fixture" rm -qf -- "$changed"
}

for chart in sparkwing-full sparkwing-runner-bundle; do
  for surface in values.yaml values.schema.json templates/runner.yaml Chart.yaml Chart.lock; do
    check_surface "charts/$chart/$surface" 1
  done
  check_surface "charts/$chart/README.md" 0
done
check_surface charts/sparkwing-full/charts/sparkwing-runner-bundle-0.1.7.tgz 1
check_surface internal/runners/k8s/k8s.go 1
check_surface internal/runners/k8s/k8s_test.go 0
check_surface internal/orchestrator/dispatch.go 0
check_surface internal/configref/configref.go 0
check_surface pkg/docs/mirror/guide.md 0
check_surface pkg/docs/changelog.md 0
check_surface pkg/docs/docs.go 1
# git rm prunes the directory those cases emptied, and the mirror sync below writes into it.
mkdir -p "$fixture/pkg/docs"
check_surface charts/vendor_test.go 0
check_surface charts/testdata/values.yaml 0

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
if [[ "$output" != *"check-changelog: docs/changes/<slug>.md entry required"* ]] ||
   [[ "$output" != *"$changed"* ]]; then
  echo "check-changelog-test: covered filename did not produce the policy diagnostic" >&2
  printf '%s\n' "$output" >&2
  exit 1
fi

mkdir -p "$fixture/docs/changes"
printf '### Added\n\n- **fixture:** Added entry\n' > "$fixture/docs/changes/test.md"
/bin/bash "$fixture/bin/check-changelog.sh"

# The working directory here is another repository, so a copy that resolved
# its root from the cwd would sync that tree instead of its own.
rm -rf "$fixture/pkg/docs/mirror"
/bin/bash "$fixture/bin/sync-docs.sh" >/dev/null
if [ ! -f "$fixture/pkg/docs/mirror/index.md" ]; then
  echo "check-changelog-test: sync-docs did not anchor on its own location" >&2
  exit 1
fi

stamp="$fixture/sync-stamp"
touch -t 200001010000 "$stamp"
for path in "$fixture/pkg/docs/mirror" "$fixture/pkg/docs/mirror/index.md" "$fixture/pkg/docs/changelog.md"; do
  touch -r "$stamp" "$path"
done
/bin/bash "$fixture/bin/sync-docs.sh" >/dev/null
for path in "$fixture/pkg/docs/mirror" "$fixture/pkg/docs/mirror/index.md" "$fixture/pkg/docs/changelog.md"; do
  if [[ "$path" -nt "$stamp" || "$stamp" -nt "$path" ]]; then
    echo "check-changelog-test: unchanged sync rewrote $path" >&2
    exit 1
  fi
done

printf 'updated docs\n' > "$fixture/docs/index.md"
mkdir -p "$fixture/docs/nested"
printf 'new page\n' > "$fixture/docs/nested/new.md"
printf 'stale page\n' > "$fixture/pkg/docs/mirror/stale.md"
/bin/bash "$fixture/bin/sync-docs.sh" >/dev/null
if ! diff -rq "$fixture/docs" "$fixture/pkg/docs/mirror"; then
  echo "check-changelog-test: sync did not update, add, and remove mirror pages" >&2
  exit 1
fi
if [[ "$fixture/pkg/docs/changelog.md" -nt "$stamp" ]]; then
  echo "check-changelog-test: docs update rewrote the unchanged changelog" >&2
  exit 1
fi

touch -r "$stamp" "$fixture/pkg/docs/mirror/index.md"
printf '\n- Another change.\n' >> "$fixture/CHANGELOG.md"
/bin/bash "$fixture/bin/sync-docs.sh" >/dev/null
if ! cmp -s "$fixture/CHANGELOG.md" "$fixture/pkg/docs/changelog.md"; then
  echo "check-changelog-test: sync did not update the changelog" >&2
  exit 1
fi
if [[ "$fixture/pkg/docs/mirror/index.md" -nt "$stamp" ]]; then
  echo "check-changelog-test: changelog update rewrote the unchanged docs" >&2
  exit 1
fi
/bin/bash "$fixture/bin/sync-docs.sh" --check >/dev/null


for drift in missing different; do
  if [[ "$drift" == missing ]]; then rm "$fixture/pkg/docs/mirror/changes/test.md";
  else printf 'wrong fragment\n' > "$fixture/pkg/docs/mirror/changes/test.md"; fi
  if /bin/bash "$fixture/bin/sync-docs.sh" --check >/dev/null 2>&1; then
    echo "check-changelog-test: $drift fragment mirror accepted" >&2
    exit 1
  fi
  /bin/bash "$fixture/bin/sync-docs.sh" >/dev/null
done

printf '\n### Fixed\n\n- shared entry\n' >> "$fixture/CHANGELOG.md"
if /bin/bash "$fixture/bin/check-changelog.sh" >/dev/null 2>&1; then
  echo "check-changelog-test: shared unreleased edits accepted" >&2
  exit 1
fi
git -C "$fixture" show HEAD:CHANGELOG.md > "$fixture/CHANGELOG.md"
rm "$fixture/docs/changes/test.md"
: > "$fixture/docs/changes/empty.md"
if /bin/bash "$fixture/bin/check-changelog.sh" >/dev/null 2>&1; then
  echo "check-changelog-test: empty fragment accepted" >&2
  exit 1
fi
printf 'check-changelog-test: passed\n'
