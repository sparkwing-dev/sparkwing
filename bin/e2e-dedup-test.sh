#!/usr/bin/env bash
# wing: e2e-dedup-test
# desc: E2E tests for work deduplication (claim/waiter/status lifecycle)
# arg: controller (optional, default: http://localhost:9001) Controller URL
set -uo pipefail

CONTROLLER="${SPARKWING_CONTROLLER:-http://localhost:9001}"

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

echo -e "${BOLD}Sparkwing work deduplication E2E tests${RESET}"
echo -e "Controller: ${CYAN}$CONTROLLER${RESET}"
echo ""

# --- 1. Claim a work lock ---
echo -e "${BOLD}1. Claim work lock${RESET}"
CLAIM=$(curl -s -X POST "$CONTROLLER/work/claim?key=e2e-test:abc123&job=e2e-owner" 2>&1)
assert "claim returns JSON" '"owner":' "$CLAIM"
assert "first claimer is owner" '"owner":true' "$CLAIM"

# --- 2. Second claim returns waiter ---
echo -e "${BOLD}2. Second claim is waiter${RESET}"
CLAIM2=$(curl -s -X POST "$CONTROLLER/work/claim?key=e2e-test:abc123&job=e2e-waiter" 2>&1)
assert "second claim returns JSON" '"owner":' "$CLAIM2"
assert "second claimer is NOT owner" '"owner":false' "$CLAIM2"
assert "waiter sees owner job ID" '"owner_job_id":"e2e-owner"' "$CLAIM2"

# --- 3. Status shows running ---
echo -e "${BOLD}3. Status while running${RESET}"
STATUS=$(curl -s "$CONTROLLER/work/status?key=e2e-test:abc123" 2>&1)
assert "status is running" '"status":"running"' "$STATUS"
assert "status shows owner" '"owner_job_id":"e2e-owner"' "$STATUS"

# --- 4. Complete the work (success) ---
echo -e "${BOLD}4. Complete work (success)${RESET}"
COMPLETE=$(curl -s -X POST "$CONTROLLER/work/complete" \
  -H "Content-Type: application/json" \
  -d '{"key":"e2e-test:abc123","success":true,"result":{"tests_passed":42}}' 2>&1)
assert "complete returns ok" '"status":"ok"' "$COMPLETE"

# --- 5. Status shows complete with result ---
echo -e "${BOLD}5. Status after completion${RESET}"
STATUS2=$(curl -s "$CONTROLLER/work/status?key=e2e-test:abc123" 2>&1)
assert "status is complete" '"status":"complete"' "$STATUS2"

# --- 6. Reclaim after completion ---
echo -e "${BOLD}6. Reclaim after completion${RESET}"
RECLAIM=$(curl -s -X POST "$CONTROLLER/work/claim?key=e2e-test:abc123&job=e2e-owner-2" 2>&1)
assert "reclaim succeeds" '"owner":true' "$RECLAIM"

# Complete the reclaimed lock so it doesn't linger
curl -s -X POST "$CONTROLLER/work/complete" \
  -H "Content-Type: application/json" \
  -d '{"key":"e2e-test:abc123","success":true,"result":{"cleanup":true}}' > /dev/null 2>&1

# --- 7. Different keys are independent ---
echo -e "${BOLD}7. Independent keys${RESET}"
CLAIM_A=$(curl -s -X POST "$CONTROLLER/work/claim?key=e2e-test:key-a&job=job-a" 2>&1)
CLAIM_B=$(curl -s -X POST "$CONTROLLER/work/claim?key=e2e-test:key-b&job=job-b" 2>&1)
assert "key-a owner" '"owner":true' "$CLAIM_A"
assert "key-b owner" '"owner":true' "$CLAIM_B"

# Cleanup
curl -s -X POST "$CONTROLLER/work/complete" -H "Content-Type: application/json" \
  -d '{"key":"e2e-test:key-a","success":true,"result":{}}' > /dev/null 2>&1
curl -s -X POST "$CONTROLLER/work/complete" -H "Content-Type: application/json" \
  -d '{"key":"e2e-test:key-b","success":true,"result":{}}' > /dev/null 2>&1

# --- 8. Complete with failure ---
echo -e "${BOLD}8. Complete with failure${RESET}"
curl -s -X POST "$CONTROLLER/work/claim?key=e2e-test:fail&job=e2e-fail-owner" > /dev/null 2>&1
FAIL_COMPLETE=$(curl -s -X POST "$CONTROLLER/work/complete" \
  -H "Content-Type: application/json" \
  -d '{"key":"e2e-test:fail","success":false,"result":{"error":"exit code 1"}}' 2>&1)
assert "fail complete returns ok" '"status":"ok"' "$FAIL_COMPLETE"

FAIL_STATUS=$(curl -s "$CONTROLLER/work/status?key=e2e-test:fail" 2>&1)
assert "failed status" '"status":"failed"' "$FAIL_STATUS"

# --- 9. Status of unknown key ---
echo -e "${BOLD}9. Unknown key${RESET}"
UNKNOWN=$(curl -s "$CONTROLLER/work/status?key=e2e-test:nonexistent" 2>&1)
assert "unknown key returns none" '"status":"none"' "$UNKNOWN"

# --- 10. Error handling ---
echo -e "${BOLD}10. Error handling${RESET}"
ERR_CLAIM=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$CONTROLLER/work/claim" 2>&1)
assert "claim without params returns 400" "400" "$ERR_CLAIM"

ERR_METHOD=$(curl -s -o /dev/null -w "%{http_code}" "$CONTROLLER/work/claim?key=x&job=y" 2>&1)
assert "GET to claim returns 405" "405" "$ERR_METHOD"

ERR_COMPLETE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$CONTROLLER/work/complete" \
  -H "Content-Type: application/json" -d 'not json' 2>&1)
assert "bad JSON to complete returns 400" "400" "$ERR_COMPLETE"

ERR_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$CONTROLLER/work/status" 2>&1)
assert "status without key returns 400" "400" "$ERR_STATUS"

# --- 11. Concurrent claim race (shell-level) ---
echo -e "${BOLD}11. Concurrent claims${RESET}"
KEY="e2e-test:race-$(date +%s)"
TMPDIR_RACE=$(mktemp -d)
for i in $(seq 1 5); do
  curl -s -X POST "$CONTROLLER/work/claim?key=$KEY&job=racer-$i" > "$TMPDIR_RACE/result-$i.json" 2>&1 &
done
wait

OWNER_COUNT=0
for i in $(seq 1 5); do
  if grep -q '"owner":true' "$TMPDIR_RACE/result-$i.json" 2>/dev/null; then
    OWNER_COUNT=$((OWNER_COUNT + 1))
  fi
done
rm -rf "$TMPDIR_RACE"

assert "exactly one owner in race" "1" "$OWNER_COUNT"

# Cleanup race key
curl -s -X POST "$CONTROLLER/work/complete" -H "Content-Type: application/json" \
  -d "{\"key\":\"$KEY\",\"success\":true,\"result\":{}}" > /dev/null 2>&1

# --- Summary ---
echo ""
TOTAL=$((PASSED + FAILED))
echo -e "${BOLD}Results${RESET}"
echo -e "  ${GREEN}${PASSED} passed${RESET}  ${RED}${FAILED} failed${RESET}  ${DIM}${TOTAL} total${RESET}"

if [[ $FAILED -eq 0 ]]; then
  echo -e "\n${GREEN}${BOLD}All work dedup E2E tests passed.${RESET}"
else
  echo -e "\n${RED}${BOLD}${FAILED} E2E test(s) failed.${RESET}"
  exit 1
fi
