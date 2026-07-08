#!/usr/bin/env bash
# Extract a versioned section from CHANGELOG.md so the release
# workflow can feed it to `gh release create --notes-file -`.
#
# Usage:
#   bash bin/extract-changelog-section.sh <version> [changelog-path]
#
# Matches `## [<version>]` headings (with or without a ` - YYYY-MM-DD`
# suffix) and prints everything between that heading and the next
# top-level `## ` heading. The heading line itself is NOT printed --
# the GitHub Release title already names the version.
#
# Trims leading + trailing blank lines so the body starts/ends at
# the first/last content line. Exits non-zero with a clear message
# when the section can't be found.

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

# Trim leading + trailing blank lines so the printed body starts at
# the first content line. Internal blank lines are preserved.
printf '%s\n' "$body" | awk '
  /^$/ { if (!started) next; blank++; next }
  {
    if (!started) { started = 1 }
    while (blank-- > 0) print ""
    blank = 0
    print
  }
'
