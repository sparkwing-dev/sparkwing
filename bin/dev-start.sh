#!/usr/bin/env bash
# Start the local dashboard dev loop:
#   * sparkwing-local-ws on :4343 -- serves /api/v1/* off the local
#     SQLite store at ~/.sparkwing/state.db (the same store wing
#     writes to).
#   * next dev on :3100 -- serves the SPA with HMR. The dev rewrite
#     in web/next.config.ts proxies /api/* to :4343, so a UI change
#     hot-reloads in <1s without rebuilding the Go binary.
#
# Iteration loop: edit a .tsx, see it in the browser. Edit Go code,
# bash bin/install.sh && bash bin/dev-restart.sh.
#
# Logs land in /tmp/sparkwing-dev/{local-ws,web}.log; PIDs in
# /tmp/sparkwing-dev/*.pid. bash bin/dev-stop.sh stops both.

set -uo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
RUN_DIR="/tmp/sparkwing-dev"
mkdir -p "$RUN_DIR"

log_local_ws="$RUN_DIR/local-ws.log"
log_web="$RUN_DIR/web.log"
pid_local_ws="$RUN_DIR/local-ws.pid"
pid_web="$RUN_DIR/web.pid"

# alive PID? helper -- treats stale pidfiles as "not running" so we
# never kill an unrelated process by recycled pid.
alive() {
  local pidfile="$1"
  [ -f "$pidfile" ] || return 1
  local pid
  pid=$(cat "$pidfile" 2>/dev/null) || return 1
  [ -n "$pid" ] || return 1
  kill -0 "$pid" 2>/dev/null
}

if alive "$pid_local_ws" && alive "$pid_web"; then
  echo "==> already running:"
  echo "    sparkwing-local-ws (pid $(cat "$pid_local_ws")) -- log: $log_local_ws"
  echo "    next dev           (pid $(cat "$pid_web"))       -- log: $log_web"
  echo "    open http://localhost:3100"
  exit 0
fi

# Best-effort cleanup of half-running state from a previous start that
# died mid-launch -- we don't want a stale pidfile to silently mask a
# fresh start failure.
bash "$REPO/bin/dev-stop.sh" >/dev/null 2>&1 || true

if ! command -v sparkwing-local-ws >/dev/null 2>&1; then
  echo "error: sparkwing-local-ws not on PATH; run 'bash bin/install.sh' first" >&2
  exit 1
fi

echo "==> starting sparkwing-local-ws on :4343 (log: $log_local_ws)"
sparkwing-local-ws >"$log_local_ws" 2>&1 &
echo $! >"$pid_local_ws"

echo "==> starting next dev on :3100 (log: $log_web)"
(cd "$REPO/web" && npm run dev) >"$log_web" 2>&1 &
echo $! >"$pid_web"

# Brief settle so we can warn loudly if either died on startup, before
# the user notices via a 502 in the browser.
sleep 2
if ! alive "$pid_local_ws"; then
  echo "error: sparkwing-local-ws exited; tail $log_local_ws" >&2
  rm -f "$pid_local_ws"
fi
if ! alive "$pid_web"; then
  echo "error: next dev exited; tail $log_web" >&2
  rm -f "$pid_web"
fi

echo
echo "==> dashboard:"
echo "    http://localhost:3100/runs"
echo
echo "==> stop with: bash bin/dev-stop.sh"
echo "==> tail logs: tail -f $log_local_ws    or    tail -f $log_web"
