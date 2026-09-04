#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

usage() {
  cat <<'EOF'
usage: bash bin/check-changelog.sh [--fix]

  (no flags)  reject the doubled release heading a union merge of two
              competing [Unreleased] renames leaves behind, then require a
              CHANGELOG.md entry when the diff touches a covered surface
  --fix       collapse duplicate ### headings under [Unreleased] into one
              block per category, keeping bullets in landing order, then
              re-sync the embedded mirror
  -h, --help  this message
EOF
}

# The union merge driver CHANGELOG.md carries (.gitattributes) keeps both sides
# of every hunk, so two branches that each rename [Unreleased] to their own
# version leave both headings stacked on adjacent lines. Nothing downstream
# reads that as an error, so catch it here.
check_stacked_headings() {
  local stacked
  stacked="$(
    awk '
      /^## / {
        if (prev != "") { print prevLine ": " prev; print NR ": " $0 }
        prev = $0
        prevLine = NR
        next
      }
      { prev = "" }
    ' CHANGELOG.md
  )"
  if [[ -z "$stacked" ]]; then
    return 0
  fi
  echo "check-changelog: two release headings sit on adjacent lines:" >&2
  sed 's/^/  /' <<<"$stacked" >&2
  echo "" >&2
  echo "A union merge stacked them. Keep the heading this release is cutting," >&2
  echo "delete the other, and run bash bin/sync-docs.sh." >&2
  exit 1
}

fix_unreleased() {
  check_stacked_headings
  local dups
  dups="$(
    awk '
      /^## \[Unreleased\]/ { in_section = 1; next }
      in_section && /^## / { in_section = 0 }
      in_section && /^### / { print }
    ' CHANGELOG.md | sort | uniq -d
  )"
  if [[ -z "$dups" ]]; then
    echo "check-changelog --fix: [Unreleased] already has one block per category"
    return 0
  fi

  local tmp
  tmp="$(mktemp)"
  awk '
    function trim(s) {
      sub(/^\n+/, "", s)
      sub(/\n+$/, "", s)
      return s
    }
    function flush(  text) {
      text = trim(buf)
      buf = ""
      if (text == "") { return }
      if (category == "") {
        preamble = preamble (preamble == "" ? "" : "\n") text
      } else {
        body[category] = body[category] (body[category] == "" ? "" : "\n") text
      }
    }
    function emit(  i, c) {
      if (preamble != "") { print preamble; print "" }
      else if (count > 0) { print "" }
      for (i = 1; i <= count; i++) {
        c = order[i]
        print "### " c
        print ""
        if (body[c] != "") { print body[c]; print "" }
      }
    }
    in_section && /^## / { flush(); emit(); in_section = 0; print; next }
    in_section && /^### / {
      flush()
      category = substr($0, 5)
      if (!(category in body)) { body[category] = ""; order[++count] = category }
      next
    }
    in_section { buf = buf $0 "\n"; next }
    /^## \[Unreleased\]/ { print; in_section = 1; category = ""; next }
    { print }
    END { if (in_section) { flush(); emit() } }
  ' CHANGELOG.md > "$tmp"
  mv "$tmp" CHANGELOG.md
  bash bin/sync-docs.sh >/dev/null

  echo "check-changelog --fix: collapsed duplicate headings under [Unreleased]:"
  sed 's/^/  /' <<<"$dups"
}

for arg in "$@"; do
  case "$arg" in
    --fix)
      fix_unreleased
      exit 0
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "check-changelog: unknown argument: $arg" >&2
      usage >&2
      exit 2
      ;;
  esac
done

check_stacked_headings

base="${BASE_REF:-}"
if [[ -z "$base" ]]; then
  if git rev-parse --verify --quiet origin/main >/dev/null; then
    base="origin/main"
  elif git rev-parse --verify --quiet HEAD~1 >/dev/null; then
    base="HEAD~1"
  else
    echo "check-changelog: no comparison base (no origin/main, no HEAD~1); skipping"
    exit 0
  fi
fi

changed=()
while IFS= read -r -d '' f; do
  changed+=("$f")
done < <(
  {
    git diff --name-only -z "$base"...HEAD
    git diff --name-only -z HEAD
    git diff --name-only -z --cached
  } | sort -zu
)

if [[ ${#changed[@]} -eq 0 ]]; then
  exit 0
fi

is_covered() {
  local f="$1"
  case "$f" in
    *_test.go)            return 1 ;;
    */testdata/*)         return 1 ;;
    internal/*)           return 1 ;;
    docs/*|examples/*)    return 1 ;;
    bench/*|build/*)      return 1 ;;
    charts/*|install/*)   return 1 ;;
    web/*|node_modules/*) return 1 ;;
    pkg/*.go|pkg/*/*)     return 0 ;;
    sparkwing/*.go|sparkwing/*/*) return 0 ;;
    cmd/*/*.go)           return 0 ;;
  esac
  return 1
}

covered_changes=()
changelog_touched=false
for f in "${changed[@]}"; do
  [[ -z "$f" ]] && continue
  if [[ "$f" == "CHANGELOG.md" ]]; then
    changelog_touched=true
    continue
  fi
  if is_covered "$f"; then
    covered_changes+=("$f")
  fi
done

if [[ ${#covered_changes[@]} -eq 0 ]]; then
  exit 0
fi

if [[ "$changelog_touched" == "true" ]]; then
  added=$(
    {
      git diff "$base"...HEAD -- CHANGELOG.md
      git diff HEAD -- CHANGELOG.md
      git diff --cached -- CHANGELOG.md
    } | awk '
      /^@@/ { in_hunk=1; next }
      in_hunk && /^\+[^+]/ { print substr($0, 2) }
    ' | grep -v '^[[:space:]]*$' || true
  )
  if [[ -n "$added" ]]; then
    exit 0
  fi
fi

echo "check-changelog: CHANGELOG.md update required" >&2
echo "" >&2
echo "Files changed on covered surfaces (per VERSIONING.md):" >&2
for f in "${covered_changes[@]}"; do
  echo "  $f" >&2
done
echo "" >&2
echo "Add an entry under the [Unreleased] section of CHANGELOG.md," >&2
echo "then re-run. See VERSIONING.md for what counts as a breaking" >&2
echo "change and how to phrase entries." >&2
exit 1
