#!/usr/bin/env bash
# Stop the dashboard dev loop started by bin/dev-start.sh.
#
# Idempotent: missing/stale pidfiles are silently ignored so this can
# be the first thing dev-start.sh runs to clean up leftover state from
# a previous crash.

set -uo pipefail

RUN_DIR="/tmp/sparkwing-dev"
pid_web="$RUN_DIR/web.pid"

# Sparkwing dashboard manages its own pid file; defer to its kill verb,
# which is idempotent ("dashboard not running" is a clean exit 0).
if command -v sparkwing >/dev/null 2>&1; then
  echo "==> sparkwing dashboard: stopping"
  sparkwing dashboard kill || true
else
  echo "==> sparkwing not on PATH; skipping dashboard kill"
fi

stop_next_dev() {
  local pidfile="$pid_web"
  local label="next dev"
  [ -f "$pidfile" ] || { echo "==> $label: not running"; return 0; }
  local pid
  pid=$(cat "$pidfile" 2>/dev/null) || true
  if [ -z "${pid:-}" ]; then
    rm -f "$pidfile"
    echo "==> $label: stale pidfile, cleaned"
    return 0
  fi
  if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$pidfile"
    echo "==> $label: pid $pid not alive, cleaned pidfile"
    return 0
  fi
  echo "==> $label: stopping pid $pid"
  # SIGTERM gives next dev a chance to flush its terminal restoration.
  # SIGKILL after 3s if it ignores us. We also kill the process group
  # so child processes (next dev's worker procs) are cleaned up.
  if kill -- -"$pid" 2>/dev/null; then
    :
  else
    kill "$pid" 2>/dev/null || true
  fi
  for _ in 1 2 3; do
    kill -0 "$pid" 2>/dev/null || break
    sleep 1
  done
  if kill -0 "$pid" 2>/dev/null; then
    echo "    pid $pid did not exit on TERM; sending KILL"
    kill -9 -"$pid" 2>/dev/null || kill -9 "$pid" 2>/dev/null || true
  fi
  rm -f "$pidfile"
}

stop_next_dev
