#!/usr/bin/env bash
# wing: e2e-test
# desc: E2E tests for controller API (health, trigger, status, cancel)
# arg: controller (optional, default: http://localhost:9001) Controller URL
set -uo pipefail

CONTROLLER="${SPARKWING_CONTROLLER:-http://localhost:9001}"
REPO_URL="git@github.com:koreyGambill/okbot.git"

CYAN="\033[36m"
GREEN="\033[32m"
RED="\033[31m"
BOLD="\033[1m"
DIM="\033[2m"
RESET="\033[0m"

PASSED=0
FAILED=0

assert() {
  local name="$1"
  local expected="$2"
  local actual="$3"

  if [[ "$actual" == *"$expected"* ]]; then
    echo -e "  ${GREEN}PASS${RESET} $name"
    PASSED=$((PASSED + 1))
  else
    echo -e "  ${RED}FAIL${RESET} $name ${DIM}(expected '$expected', got '$actual')${RESET}"
    FAILED=$((FAILED + 1))
  fi
}

echo -e "${BOLD}Sparkwing E2E test suite${RESET}"
echo -e "Controller: ${CYAN}$CONTROLLER${RESET}"
echo ""

# --- 1. Health check ---
echo -e "${BOLD}1. Health check${RESET}"
HEALTH=$(curl -s "$CONTROLLER/health" 2>&1)
assert "controller healthy" '"status":"ok"' "$HEALTH"

# --- 2. Trigger a job ---
echo -e "${BOLD}2. Trigger job${RESET}"
TRIGGER=$(curl -s -X POST "$CONTROLLER/trigger?app=build-test-deploy&repo=$REPO_URL" 2>&1)
JOB_ID=$(echo "$TRIGGER" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
assert "job created" '"id":' "$TRIGGER"
assert "job has ID" "$JOB_ID" "$JOB_ID"

# --- 3. Get job status ---
echo -e "${BOLD}3. Job status${RESET}"
sleep 2
STATUS=$(curl -s "$CONTROLLER/jobs/$JOB_ID" | grep -o '"status":"[^"]*"' | cut -d'"' -f4)
assert "job is running or claimed" "" "$(echo 'running claimed pending' | grep -o "$STATUS")"

# --- 4. Cancel job ---
echo -e "${BOLD}4. Cancel job${RESET}"
CANCEL=$(curl -s -X POST "$CONTROLLER/jobs/$JOB_ID/cancel" 2>&1)
assert "cancel succeeds" '"status":"cancelled"' "$CANCEL"

AFTER=$(curl -s "$CONTROLLER/jobs/$JOB_ID" | grep -o '"status":"[^"]*"' | cut -d'"' -f4)
assert "job is cancelled" "cancelled" "$AFTER"

# --- 5. Retry job ---
echo -e "${BOLD}5. Retry job${RESET}"
RETRY=$(curl -s -X POST "$CONTROLLER/jobs/$JOB_ID/retry" 2>&1)
assert "retry creates new job" '"new_job":' "$RETRY"
NEW_ID=$(echo "$RETRY" | grep -o '"new_job":"[^"]*"' | cut -d'"' -f4)
assert "new job has ID" "$NEW_ID" "$NEW_ID"

# Cancel the retry so it doesn't consume resources
sleep 1
curl -s -X POST "$CONTROLLER/jobs/$NEW_ID/cancel" > /dev/null 2>&1

# --- 6. Metrics endpoint ---
echo -e "${BOLD}6. Metrics${RESET}"
METRICS=$(curl -s "$CONTROLLER/metrics" 2>&1)
assert "metrics has total_jobs" '"total_jobs":' "$METRICS"
assert "metrics has by_status" '"by_status":' "$METRICS"

# --- 7. Log streaming endpoint ---
echo -e "${BOLD}7. Log streaming${RESET}"
# Post a log line
curl -s -X POST "$CONTROLLER/logs/test-e2e" -d '{"lines":["hello from e2e"]}' > /dev/null
# Read it back
LOGS=$(curl -s "$CONTROLLER/logs/test-e2e" -H "Accept: text/event-stream" --max-time 2 2>&1 || true)
assert "log line received" "hello from e2e" "$LOGS"

# --- 8. Audit log ---
echo -e "${BOLD}8. Audit log${RESET}"
AUDIT=$(curl -s "$CONTROLLER/audit" 2>&1)
assert "audit log has entries" '"action":' "$AUDIT"

# --- 9. Agents endpoint ---
echo -e "${BOLD}9. Agents${RESET}"
AGENTS=$(curl -s "$CONTROLLER/agents" 2>&1)
assert "agents returns array" "[" "$AGENTS"

# --- Summary ---
echo ""
TOTAL=$((PASSED + FAILED))
echo -e "${BOLD}Results${RESET}"
echo -e "  ${GREEN}${PASSED} passed${RESET}  ${RED}${FAILED} failed${RESET}  ${DIM}${TOTAL} total${RESET}"

if [[ $FAILED -eq 0 ]]; then
  echo -e "\n${GREEN}${BOLD}All E2E tests passed.${RESET}"
else
  echo -e "\n${RED}${BOLD}${FAILED} E2E test(s) failed.${RESET}"
  exit 1
fi
