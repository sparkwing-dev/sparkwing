#!/usr/bin/env bash

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
