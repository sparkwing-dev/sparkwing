#!/usr/bin/env bash
# Build the sparkwing CLI and install to ~/.local/bin so any previously
# installed copy is replaced.
#
# Only `sparkwing` lands on laptops. Cluster-side binaries (controller,
# runner, cache, logs, web) ship as Docker images. The old standalone
# `sparkwing-local-ws` daemon is superseded by `sparkwing dashboard
# start` -- if a stale copy is present in $DEST we delete it so the
# user's PATH stops resolving the older binary.
#
# Rebuilds the dashboard SPA (web/ -> internal/web/next-out) before
# the Go build so the embedded bundle is always current. Set
# SKIP_WEB_BUILD=1 to skip when iterating only on Go code and the
# bundle is already populated.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# The default destination hangs off $HOME, which a stripped CI or
# launchd context may not set; say so plainly instead of dying on
# set -u's "HOME: unbound variable".
if [ -z "${SPARKWING_INSTALL_BIN:-}" ] && [ -z "${HOME:-}" ]; then
  echo "install.sh: HOME is unset; set SPARKWING_INSTALL_BIN to choose an install directory" >&2
  exit 1
fi
DEST="${SPARKWING_INSTALL_BIN:-$HOME/.local/bin}"
mkdir -p "$DEST"

# GOPRIVATE so freshly-tagged sparks/sdk modules resolve directly from
# GitHub if proxy lags.
export GOPRIVATE='github.com/sparkwing-dev/*'

if [ "${SKIP_WEB_BUILD:-0}" = "1" ]; then
  echo "SKIP_WEB_BUILD=1 set; using existing internal/web/next-out/ as-is"
else
  bash "$ROOT/bin/build-web.sh"
fi

# Stamp an explicit v0.x dev version. Without -X main.Version the CLI falls
# through to Go's tag-derived pseudo-version, which bases off the highest
# semver tag -- the load-bearing v1.6.1 tombstone -- and reports a spurious
# v1.6.2. Derive the base from the newest published v0.* tag so it tracks
# releases without hardcoding, and mark uncommitted trees +dirty.
BASE="$(git -C "$ROOT" tag -l 'v0.*' | sort -V | tail -1)"
[ -n "$BASE" ] || BASE="v0.0.0"
VERSION="$BASE-dev+$(git -C "$ROOT" rev-parse --short HEAD)"
if ! git -C "$ROOT" diff --quiet HEAD 2>/dev/null; then
  VERSION="$VERSION+dirty"
fi

echo "build sparkwing $VERSION"
go -C "$ROOT" build -ldflags "-X main.Version=$VERSION" -o "$DEST/sparkwing" ./cmd/sparkwing

# Sweep deprecated / cluster-only binaries out of $DEST. This script's
# write authority ends at $DEST: a stale copy anywhere else -- notably
# $GOPATH/bin, where a prior `go install ./cmd/...` dropped them -- is
# reported below with the exact rm that removes it, never touched.
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
# `go install` writes to the FIRST element of GOPATH, which may be a
# colon-separated list; appending /bin to the whole list would name a
# directory that does not exist.
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

# Report -- never touch -- any other sparkwing binary reachable on this
# machine. PATH is not one list: an interactive shell orders it from
# the shell profile while a launchd job, cron entry, or systemd unit
# carries its own, so a second copy means the same command can be two
# different builds depending on who calls it. Which copy to keep is the
# operator's call; each install keeps its own version memory under
# ~/.sparkwing, so the copies cannot corrupt each other's records.
# The report never fails the install: the install already succeeded.
report_competing_installs() {
  local installed="$DEST/sparkwing"
  local -a scan_dirs=() reported=()
  local d p r dup found=0
  IFS=':' read -r -a scan_dirs <<< "${PATH:-}"
  # $HOME and $gopath_bin are optional: a HOME-less caller still gets
  # the PATH-derived scan, and set -u must never fail a report that
  # runs after the install already succeeded.
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
    # ${arr[@]+...} keeps the empty-array expansion safe under bash
    # 3.2's set -u, which macOS still ships.
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
    # %q keeps each path one Bash word. Both directions guard every kind
    # of existing directory entry (including a dangling symlink), so a
    # repeated remedy or undo cannot overwrite a file that appeared later.
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
