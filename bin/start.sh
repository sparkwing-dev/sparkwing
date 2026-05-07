#!/usr/bin/env bash
# wing: start
# desc: Start local dev environment (port-forwards, dashboard, agent)
# arg: env (required) Environment mode: local

# USAGE: ./bin/start.sh <env>
#
#   local  — port-forwards + web dashboard + local agent
#

if [[ -z "$1" ]]; then
  echo "Usage: ./bin/start.sh <env>"
  echo ""
  echo "  local  — port-forwards + web dashboard + local agent"
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

case "$1" in
  local)
    # Kill any stale port-forwards
    lsof -ti :9000,:9001,:9002,:9003 2>/dev/null | xargs kill -9 2>/dev/null || true
    lsof -ti :3100 2>/dev/null | xargs kill -9 2>/dev/null || true
    sleep 1

    # Port forwards
    "${REPO_ROOT}/bin/port-forward.sh"

    # Start web dashboard
    rm -rf "${REPO_ROOT}/web/.next"
    npm --prefix "${REPO_ROOT}/web" run dev
    ;;
esac
