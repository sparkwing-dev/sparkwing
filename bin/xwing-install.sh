#!/usr/bin/env bash
set -euo pipefail

: "${XWING_TOOL_SOURCE:?Xwing must provide the selected checkout}"
: "${XWING_TOOL_DEST:?Xwing must provide the staging executable path}"
case "$XWING_TOOL_SOURCE" in /*) ;; *) echo "candidate source must be absolute" >&2; exit 1 ;; esac
case "$XWING_TOOL_DEST" in /*) ;; *) echo "candidate destination must be absolute" >&2; exit 1 ;; esac
if [[ ! "$XWING_TOOL_SOURCE/bin/xwing-install.sh" -ef "$0" ]]; then
  echo "candidate source does not own this install hook" >&2
  exit 1
fi
if [[ -e "$XWING_TOOL_DEST" || -L "$XWING_TOOL_DEST" ]]; then
  echo "candidate destination already exists" >&2
  exit 1
fi
if [[ ! -d "$(dirname "$XWING_TOOL_DEST")" ]]; then
  echo "candidate staging directory does not exist" >&2
  exit 1
fi

stage="$(mktemp -d "$(dirname "$XWING_TOOL_DEST")/.sparkwing-build.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
NEXT_PUBLIC_SPARKWING_UI_PROTOTYPE=1 GOWORK=off SKIP_WEB_BUILD=0 SPARKWING_INSTALL_BIN="$stage" \
  SPARKWING_INSTALL_NAME=sparkwing-candidate bash "$XWING_TOOL_SOURCE/bin/install.sh"
mv "$stage/sparkwing-candidate" "$XWING_TOOL_DEST"
