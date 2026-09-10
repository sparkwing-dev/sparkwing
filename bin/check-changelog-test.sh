#!/usr/bin/env bash
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
    if [[ "$output" != *"CHANGELOG.md update required"* || "$output" != *"$changed"* ]]; then
      printf 'check-changelog-test: missing covered-surface diagnostic for %s\n%s\n' "$changed" "$output" >&2
      exit 1
    fi
    printf '\n- Changed fixture default.\n' >> "$fixture/CHANGELOG.md"
    /bin/bash "$fixture/bin/check-changelog.sh"
    git -C "$fixture" show HEAD:CHANGELOG.md > "$fixture/CHANGELOG.md"
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
if [[ "$output" != *"check-changelog: CHANGELOG.md update required"* ]] ||
   [[ "$output" != *"$changed"* ]]; then
  echo "check-changelog-test: covered filename did not produce the policy diagnostic" >&2
  printf '%s\n' "$output" >&2
  exit 1
fi

printf '\n- Added fixture.\n' >> "$fixture/CHANGELOG.md"
/bin/bash "$fixture/bin/check-changelog.sh"

cat > "$fixture/CHANGELOG.md" <<'CHANGELOG'
# Changelog

## [Unreleased]

### Security

- **store:** first landing.

### Added

- **cli:** unrelated.

### Security

- **controller:** second landing.
  Follow-up prose.

## [v0.1.0] - 2026-01-01

### Security

- **store:** released.
CHANGELOG
/bin/bash "$fixture/bin/check-changelog.sh" --fix >/dev/null

cat > "$fixture/CHANGELOG.want" <<'CHANGELOG'
# Changelog

## [Unreleased]

### Security

- **store:** first landing.
- **controller:** second landing.
  Follow-up prose.

### Added

- **cli:** unrelated.

## [v0.1.0] - 2026-01-01

### Security

- **store:** released.
CHANGELOG
if ! diff -u "$fixture/CHANGELOG.want" "$fixture/CHANGELOG.md"; then
  echo "check-changelog-test: --fix did not collapse the duplicate ### Security block" >&2
  exit 1
fi
if ! cmp -s "$fixture/CHANGELOG.md" "$fixture/pkg/docs/changelog.md"; then
  echo "check-changelog-test: --fix left the embedded changelog mirror stale" >&2
  exit 1
fi

/bin/bash "$fixture/bin/check-changelog.sh" --fix >/dev/null
if ! cmp -s "$fixture/CHANGELOG.want" "$fixture/CHANGELOG.md"; then
  echo "check-changelog-test: --fix is not idempotent" >&2
  exit 1
fi

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

# Parallel landings all add bullets under [Unreleased]. The union merge driver
# in .gitattributes has to resolve that without a conflict, and has to keep the
# embedded mirror byte-identical to the source it is copied from.
merge_fixture="$(mktemp -d "${TMPDIR:-/tmp}/sparkwing-changelog-merge.XXXXXX")"
trap 'rm -rf "$fixture" "$merge_fixture"' EXIT

mkdir -p "$merge_fixture/bin" "$merge_fixture/pkg/docs"
cp "$root/.gitattributes" "$merge_fixture/.gitattributes"
cp "$root/bin/check-changelog.sh" "$merge_fixture/bin/check-changelog.sh"
cat > "$merge_fixture/CHANGELOG.md" <<'CHANGELOG'
# Changelog

## [Unreleased]

### Added

- **cli:** baseline entry.

## [v0.1.0] - 2026-01-01

### Added

- **cli:** released.
CHANGELOG
cp "$merge_fixture/CHANGELOG.md" "$merge_fixture/pkg/docs/changelog.md"
git -C "$merge_fixture" init -q
git -C "$merge_fixture" config user.email test@example.com
git -C "$merge_fixture" config user.name test
git -C "$merge_fixture" config commit.gpgsign false
git -C "$merge_fixture" add .
git -C "$merge_fixture" commit -qm baseline
base_rev="$(git -C "$merge_fixture" rev-parse HEAD)"

land_bullet() {
  local branch="$1" bullet="$2"
  git -C "$merge_fixture" checkout -q "$base_rev"
  git -C "$merge_fixture" checkout -q -b "$branch"
  awk -v bullet="$bullet" '
    { print }
    /^- \*\*cli:\*\* baseline entry\.$/ { print bullet }
  ' "$merge_fixture/CHANGELOG.md" > "$merge_fixture/CHANGELOG.next"
  mv "$merge_fixture/CHANGELOG.next" "$merge_fixture/CHANGELOG.md"
  cp "$merge_fixture/CHANGELOG.md" "$merge_fixture/pkg/docs/changelog.md"
  git -C "$merge_fixture" commit -qam "$branch"
}

land_bullet landing-a '- **store:** entry from the first branch.'
land_bullet landing-b '- **cache:** entry from the second branch.'

git -C "$merge_fixture" checkout -q landing-a
if ! git -C "$merge_fixture" merge --no-edit landing-b >/dev/null 2>&1; then
  echo "check-changelog-test: two branches adding [Unreleased] bullets conflicted" >&2
  git -C "$merge_fixture" diff >&2
  exit 1
fi
for bullet in '- **cli:** baseline entry.' \
              '- **store:** entry from the first branch.' \
              '- **cache:** entry from the second branch.'; do
  if ! grep -qxF -- "$bullet" "$merge_fixture/CHANGELOG.md"; then
    echo "check-changelog-test: the union merge dropped: $bullet" >&2
    exit 1
  fi
done
if ! cmp -s "$merge_fixture/CHANGELOG.md" "$merge_fixture/pkg/docs/changelog.md"; then
  echo "check-changelog-test: the union merge split CHANGELOG.md from its mirror" >&2
  exit 1
fi
if ! /bin/bash "$merge_fixture/bin/check-changelog.sh"; then
  echo "check-changelog-test: the merged changelog failed the gate" >&2
  exit 1
fi

# Two branches that each rename [Unreleased] to their own version union into a
# pair of stacked headings, which the gate has to reject.
sed -i.bak 's/^## \[Unreleased\]$/## [v0.2.0] - 2026-02-01\n## [v0.3.0] - 2026-03-01/' "$merge_fixture/CHANGELOG.md"
rm -f "$merge_fixture/CHANGELOG.md.bak"
set +e
output="$(/bin/bash "$merge_fixture/bin/check-changelog.sh" 2>&1)"
status=$?
set -e
if [[ $status -ne 1 ]]; then
  echo "check-changelog-test: stacked release headings exited $status, want 1" >&2
  exit 1
fi
if [[ "$output" != *"two release headings sit on adjacent lines"* ]]; then
  echo "check-changelog-test: stacked release headings produced no diagnostic" >&2
  printf '%s\n' "$output" >&2
  exit 1
fi

echo "check-changelog-test: ok"
