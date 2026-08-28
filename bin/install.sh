#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if [ -z "${SPARKWING_INSTALL_BIN:-}" ] && [ -z "${HOME:-}" ]; then
  echo "install.sh: HOME is unset; set SPARKWING_INSTALL_BIN to choose an install directory" >&2
  exit 1
fi
DEST="${SPARKWING_INSTALL_BIN:-$HOME/.local/bin}"
mkdir -p "$DEST"

export GOPRIVATE='github.com/sparkwing-dev/*'

if [ "${SKIP_WEB_BUILD:-0}" = "1" ]; then
  echo "SKIP_WEB_BUILD=1 set; using existing internal/web/next-out/ as-is"
else
  bash "$ROOT/bin/build-web.sh"
fi

BASE="$(git -C "$ROOT" tag -l 'v0.*' | sort -V | tail -1)"
[ -n "$BASE" ] || BASE="v0.0.0"
VERSION="$BASE-dev+$(git -C "$ROOT" rev-parse --short HEAD)"
if ! git -C "$ROOT" diff --quiet HEAD 2>/dev/null; then
  VERSION="$VERSION+dirty"
fi

echo "build sparkwing $VERSION"
go -C "$ROOT" build -ldflags "-X main.Version=$VERSION" -o "$DEST/sparkwing" ./cmd/sparkwing

declare -a STALE=(
  sparkwing-cache
  sparkwing-controller
  sparkwing-local-ws
  sparkwing-logs
  sparkwing-runner
  sparkwing-web
  sparkwing.dev
  sparkwing.predeploy
)
for s in "${STALE[@]}"; do
  if [ -e "$DEST/$s" ]; then
    rm -f "$DEST/$s"
    echo "removed stale $DEST/$s"
  fi
done
gopath_all="$(go env GOPATH 2>/dev/null || true)"
gopath_bin=""
if [ -n "$gopath_all" ]; then
  gopath_bin="${gopath_all%%:*}/bin"
fi
if [ -n "$gopath_bin" ] && [ -d "$gopath_bin" ] && [ "$gopath_bin" != "$DEST" ]; then
  for s in "${STALE[@]}"; do
    if [ -e "$gopath_bin/$s" ]; then
      printf 'note: stale %s is a retired binary this install does not own; remove it with: rm %q\n' "$gopath_bin/$s" "$gopath_bin/$s"
    fi
  done
fi

report_competing_installs() {
  local installed="$DEST/sparkwing"
  local -a scan_dirs=() reported=()
  local d p r dup found=0
  IFS=':' read -r -a scan_dirs <<< "${PATH:-}"
  if [ -n "${HOME:-}" ]; then
    scan_dirs+=("$HOME/.local/bin" "$HOME/go/bin")
  fi
  if [ -n "$gopath_bin" ]; then
    scan_dirs+=("$gopath_bin")
  fi
  scan_dirs+=(/usr/local/bin /opt/homebrew/bin)
  if [ -n "${GOBIN:-}" ]; then
    scan_dirs+=("$GOBIN")
  fi

  for d in "${scan_dirs[@]}"; do
    p="$d/sparkwing"
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
  if [ "$found" = 1 ]; then
    echo "      \`sparkwing doctor\` reports the full picture."
  fi
}
report_competing_installs

echo
echo "Installed to $DEST:"
ls -1 "$DEST"/sparkwing*
