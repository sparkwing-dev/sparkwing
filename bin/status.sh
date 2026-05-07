#!/usr/bin/env bash
# wing: status
# desc: Quick status check for sparkwing controller
# arg: controller (optional, default: http://localhost:9001) Controller URL
set -euo pipefail

CONTROLLER="${SPARKWING_CONTROLLER:-http://localhost:9001}"

CYAN="\033[36m"
GREEN="\033[32m"
YELLOW="\033[33m"
RED="\033[31m"
BOLD="\033[1m"
DIM="\033[2m"
RESET="\033[0m"

case "${1:-all}" in
  agents|a)
    echo -e "${BOLD}=== agents ===${RESET}"
    curl -sf "$CONTROLLER/agents" | jq -r '.[] | "\(.name)\t\(.type)\t\(.status)\tactive=\(.active_jobs // [] | length)/\(.max_concurrent)\tjobs=\(.active_jobs // [] | join(","))"' | column -t -s$'\t'
    ;;
  jobs|j)
    echo -e "${BOLD}=== jobs ===${RESET}"
    curl -sf "$CONTROLLER/jobs" | jq -r 'sort_by(.created_at)[] | "\(.id)\t\(.app)\t\(.status)\tagent=\(.agent_id // "-")\tparent=\(.parent_id // "-")\t\(if .result then (if .result.success then "OK" else "FAIL" end) + " " + ((.result.duration / 1e9 * 10 | floor / 10 | tostring) + "s") else "" end)"' | column -t -s$'\t'
    ;;
  all|*)
    "$0" agents
    echo ""
    "$0" jobs
    ;;
esac
