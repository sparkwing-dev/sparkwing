#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
windows=0
exe_suffix=""
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) windows=1; exe_suffix=".exe" ;;
esac
shell_path() {
  cygpath -u -- "$1"
}
reuse_web=0
if [[ "${1:-}" == --reuse-web && $# == 1 ]]; then
  reuse_web=1
elif (( $# != 0 )); then
  echo "usage: install.sh [--reuse-web]" >&2
  exit 2
fi
if [ -z "${SPARKWING_INSTALL_BIN:-}" ] && [ -z "${HOME:-}" ]; then
  echo "install.sh: HOME is unset; set SPARKWING_INSTALL_BIN to choose an install directory" >&2
  exit 1
fi
DEST="${SPARKWING_INSTALL_BIN:-$HOME/.local/bin}"
if (( windows )); then
  DEST="$(shell_path "$DEST")"
fi
# A named build (SPARKWING_INSTALL_NAME=sparkwing-crons) sits beside the real
# binary and shares its home, so a branch can be exercised against real runs
# without replacing the sparkwing every other repo and the timer resolve.
NAME="${SPARKWING_INSTALL_NAME:-sparkwing}"
# The name is joined onto $DEST, so the slug is held to lowercase, digits and
# dashes: a bare `sparkwing-*` glob would accept `sparkwing-../../anything`.
if ! printf '%s' "$NAME" | grep -Eq '^sparkwing(-[a-z0-9-]+)?$'; then
  echo "install.sh: SPARKWING_INSTALL_NAME must be sparkwing or sparkwing-<slug>, got $NAME" >&2
  exit 1
fi
if (( ! windows )) || [ ! -d "$DEST" ]; then
  mkdir -p "$DEST"
fi

export GOPRIVATE='github.com/sparkwing-dev/*'

# shellcheck source=bin/web-build-lock.sh
source "$ROOT/bin/web-build-lock.sh"
sparkwing_lock_web_build "$ROOT" "$@"

if [ "${SKIP_WEB_BUILD:-0}" = "1" ]; then
  echo "SKIP_WEB_BUILD=1 set; using existing internal/web/next-out/ as-is"
elif ! command -v pnpm >/dev/null 2>&1; then
  if [ -f "$ROOT/web/out/index.html" ]; then
    echo "warning: pnpm unavailable; using prebuilt web/out for the dashboard" >&2
    rm -rf "$ROOT/internal/web/next-out"
    mkdir -p "$ROOT/internal/web/next-out"
    cp -R "$ROOT/web/out/." "$ROOT/internal/web/next-out/"
    touch "$ROOT/internal/web/next-out/.gitkeep"
  elif [ -f "$ROOT/internal/web/next-out/index.html" ]; then
    echo "warning: pnpm unavailable; using existing embedded dashboard" >&2
  else
    echo "warning: pnpm unavailable and no prebuilt web/out; dashboard unavailable in this build" >&2
  fi
else
  if (( reuse_web )); then
    "$BASH" "$ROOT/bin/build-web.sh" --reuse
  else
    "$BASH" "$ROOT/bin/build-web.sh"
  fi
fi

BASE="$(git -C "$ROOT" tag -l 'v0.*' | sort -V | tail -1)"
[ -n "$BASE" ] || BASE="v0.0.0"
VERSION="$BASE-dev+$(git -C "$ROOT" rev-parse --short HEAD)"
if ! git -C "$ROOT" diff --quiet HEAD 2>/dev/null; then
  VERSION="$VERSION+dirty"
fi

echo "build $NAME $VERSION"
# -trimpath and -s -w are the flags .github/workflows/release.yaml passes, so a
# local install and a released binary strip and trim the same way.
GOWORK=off go -C "$ROOT" build -trimpath -ldflags "-s -w -X main.Version=$VERSION" -o "$DEST/$NAME$exe_suffix" ./cmd/sparkwing
if [ "$NAME" != sparkwing ]; then
  echo
  echo "Installed to $DEST/$NAME$exe_suffix (the sparkwing beside it is untouched)"
  exit 0
fi

echo "build sparkwing-runner $VERSION"
GOWORK=off go -C "$ROOT" build -trimpath -ldflags "-s -w -X main.Version=$VERSION" -o "$DEST/sparkwing-runner$exe_suffix" ./cmd/sparkwing-runner

declare -a STALE=(
  sparkwing-cache
  sparkwing-controller
  sparkwing-local-ws
  sparkwing-logs
  sparkwing-web
  sparkwing.dev
  sparkwing.predeploy
)
if (( windows )); then
  for s in "${STALE[@]}"; do STALE+=("$s.exe"); done
fi
for s in "${STALE[@]}"; do
  if [ -e "$DEST/$s" ]; then
    rm -f "$DEST/$s"
    echo "removed stale $DEST/$s"
  fi
done
gopath_all="$(go env GOPATH 2>/dev/null || true)"
gopath_bin=""
if [ -n "$gopath_all" ]; then
  if (( windows )); then
    gopath_bin="$(shell_path "${gopath_all%%;*}")/bin"
  else
    gopath_bin="${gopath_all%%:*}/bin"
  fi
fi
if [ -n "$gopath_bin" ] && [ -d "$gopath_bin" ] && [ "$gopath_bin" != "$DEST" ]; then
  for s in "${STALE[@]}"; do
    if [ -e "$gopath_bin/$s" ]; then
      printf 'note: stale %s is a retired binary this install does not own; remove it with: rm %q\n' "$gopath_bin/$s" "$gopath_bin/$s"
    fi
  done
fi

report_competing_installs() {
  local installed="$DEST/sparkwing$exe_suffix"
  local -a scan_dirs=() reported=()
  local -a suffixes=("$exe_suffix")
  if (( windows )); then suffixes+=(""); fi
  local d p r dup suffix found=0
  IFS=':' read -r -a scan_dirs <<< "${PATH:-}"
  if [ -n "${HOME:-}" ]; then
    scan_dirs+=("$HOME/.local/bin" "$HOME/go/bin")
  fi
  if [ -n "$gopath_bin" ]; then
    scan_dirs+=("$gopath_bin")
  fi
  scan_dirs+=(/usr/local/bin /opt/homebrew/bin)
  if [ -n "${GOBIN:-}" ]; then
    if (( windows )); then
      scan_dirs+=("$(shell_path "$GOBIN")")
    else
      scan_dirs+=("$GOBIN")
    fi
  fi

  for d in "${scan_dirs[@]}"; do
    for suffix in "${suffixes[@]}"; do
    p="$d/sparkwing$suffix"
    if [ ! -f "$p" ] || [ ! -x "$p" ]; then continue; fi
    if [ "$p" -ef "$installed" ]; then continue; fi
    dup=0
    for r in ${reported[@]+"${reported[@]}"}; do
      if [ "$p" -ef "$r" ]; then
        dup=1
        break
      fi
    done
    if [ "$dup" = 1 ]; then continue; fi
    reported+=("$p")
    found=1
    echo "note: another sparkwing is installed at $p (left untouched)"
    echo "      a shell or job whose PATH orders $d before $DEST runs it instead of $installed"
    printf '      to retire it: test ! -e %q && test ! -L %q && mv -n -- %q %q && test ! -e %q && test ! -L %q   (undo: test ! -e %q && test ! -L %q && mv -n -- %q %q && test ! -e %q && test ! -L %q)\n' \
      "$p.superseded" "$p.superseded" "$p" "$p.superseded" "$p" "$p" \
      "$p" "$p" "$p.superseded" "$p" "$p.superseded" "$p.superseded"
    done
  done
  if [ "$found" = 1 ]; then
    echo "      \`sparkwing doctor\` reports the full picture."
  fi
}
report_competing_installs

echo
echo "Installed to $DEST:"
ls -1 "$DEST"/sparkwing*
