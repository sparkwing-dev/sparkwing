#!/usr/bin/env bash
# Build the sparkwing CLI and install to ~/.local/bin when this checkout is
# newer than the installed build. Parallel source installs serialize their
# final compare-and-swap, so an older worktree cannot overwrite a newer CLI.
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
REVISION="$(git -C "$ROOT" rev-parse HEAD)"
COMMIT_TIME="$(git -C "$ROOT" show -s --format=%cI HEAD)"
modified=0
if [ -n "$(git -C "$ROOT" status --porcelain --untracked-files=normal 2>/dev/null)" ]; then
  VERSION="$VERSION+dirty"
  modified=1
fi

echo "build sparkwing $VERSION"
candidate="$DEST/.sparkwing-candidate-$$"
trap 'rm -f "$candidate"' EXIT
go -C "$ROOT" build -ldflags "-X main.Version=$VERSION" -o "$candidate" ./cmd/sparkwing

guard_args=(
  --candidate "$candidate"
  --target "$DEST/sparkwing"
  --candidate-version "$VERSION"
  --candidate-revision "$REVISION"
  --candidate-time "$COMMIT_TIME"
)
if [ "$modified" = "1" ]; then
  guard_args+=(--candidate-modified)
fi
if [ "${SPARKWING_INSTALL_ALLOW_DOWNGRADE:-0}" = "1" ]; then
  guard_args+=(--allow-downgrade)
fi
go -C "$ROOT" run ./internal/installguard "${guard_args[@]}"

# Sweep deprecated / cluster-only binaries that don't belong on a user
# laptop. We check $DEST (install target) and $GOPATH/bin (where a
# prior `go install ./cmd/...` would have dropped them) so they can't
# silently resolve on PATH after install. Silent if absent.
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
declare -a SWEEP_DIRS=("$DEST")
gopath_bin="$(go env GOPATH 2>/dev/null)/bin"
if [ -d "$gopath_bin" ] && [ "$gopath_bin" != "$DEST" ]; then
  SWEEP_DIRS+=("$gopath_bin")
fi
for d in "${SWEEP_DIRS[@]}"; do
  for s in "${STALE[@]}"; do
    if [ -e "$d/$s" ]; then
      rm -f "$d/$s"
      echo "removed stale $d/$s"
    fi
  done
done

echo
echo "Installed to $DEST:"
ls -1 "$DEST/sparkwing"
