#!/usr/bin/env bash

set -euo pipefail

if [ $# -lt 1 ]; then
  echo "usage: bash bin/extract-changelog-section.sh <version> [changelog-path]" >&2
  exit 2
fi

version="$1"
path="${2:-CHANGELOG.md}"

if [ ! -f "$path" ]; then
  echo "extract-changelog-section: $path: no such file" >&2
  exit 1
fi

body=$(awk -v prefix="## [${version}]" '
  # Top-level heading line.
  substr($0, 1, 3) == "## " {
    if (in_section) { exit }
    if (substr($0, 1, length(prefix)) == prefix) {
      in_section = 1
      next
    }
    next
  }
  in_section { print }
' "$path")

if [ -z "${body//[[:space:]]/}" ]; then
  echo "extract-changelog-section: no [${version}] section found in $path" >&2
  exit 1
fi

printf '%s\n' "$body" | awk '
  /^$/ { if (!started) next; blank++; next }
  {
    if (!started) { started = 1 }
    while (blank-- > 0) print ""
    blank = 0
    print
  }
'
