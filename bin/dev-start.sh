#!/usr/bin/env bash
# Start the local dashboard dev loop:
#   * sparkwing dashboard start -- detached supervisor on :4343 that
#     serves /api/v1/* off the local SQLite store at ~/.sparkwing/state.db
#     (the same store sparkwing writes to). Lifecycle managed by sparkwing.
#   * next dev on :3100 -- serves the SPA with HMR. The dev rewrite in
#     web/next.config.ts proxies /api/* to :4343, so a UI change hot-
#     reloads in <1s without rebuilding the Go binary.
#
# Iteration loop: edit a .tsx, see it in the browser. Edit Go code,
# bash bin/install.sh && bash bin/dev-restart.sh.
#
# Logs land in /tmp/sparkwing-dev/web.log (next dev) and
# ~/.sparkwing/dashboard.log (supervisor). PIDs for next dev live in
# /tmp/sparkwing-dev/web.pid; the supervisor manages its own pid file
# at ~/.sparkwing/dashboard.pid via `sparkwing dashboard status`.
# bash bin/dev-stop.sh stops both.

set -uo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
RUN_DIR="/tmp/sparkwing-dev"
mkdir -p "$RUN_DIR"

log_web="$RUN_DIR/web.log"
pid_web="$RUN_DIR/web.pid"

alive() {
  local pidfile="$1"
  [ -f "$pidfile" ] || return 1
  local pid
  pid=$(cat "$pidfile" 2>/dev/null) || return 1
  [ -n "$pid" ] || return 1
  kill -0 "$pid" 2>/dev/null
}

# Best-effort cleanup of half-running state from a previous start that
# died mid-launch -- we don't want a stale pidfile to silently mask a
# fresh start failure.
bash "$REPO/bin/dev-stop.sh" >/dev/null 2>&1 || true

if ! command -v sparkwing >/dev/null 2>&1; then
  echo "error: sparkwing not on PATH; run 'bash bin/install.sh' first" >&2
  exit 1
fi

echo "==> starting sparkwing dashboard on :4343"
sparkwing dashboard start

echo "==> starting next dev on :3100 (log: $log_web)"
(cd "$REPO/web" && npm run dev) >"$log_web" 2>&1 &
echo $! >"$pid_web"

sleep 2
if ! alive "$pid_web"; then
  echo "error: next dev exited; tail $log_web" >&2
  rm -f "$pid_web"
fi

echo
echo "==> dashboard:"
echo "    http://localhost:3100/runs    (next dev, hot-reload)"
echo "    http://127.0.0.1:4343/runs    (production SPA bundle)"
echo
echo "==> stop with: bash bin/dev-stop.sh"
echo "==> tail logs: tail -f $log_web    or    tail -f ~/.sparkwing/dashboard.log"
