#!/usr/bin/env bash
# wing: security-test
# desc: Security E2E tests (branch enforcement, SHA validation, webhooks, rate limiting)
# arg: controller (optional, default: http://localhost:9001) Controller URL
set -uo pipefail

CONTROLLER="${SPARKWING_CONTROLLER:-http://localhost:9001}"
REPO="koreyGambill/okbot"

CYAN="\033[36m"
GREEN="\033[32m"
RED="\033[31m"
YELLOW="\033[33m"
BOLD="\033[1m"
DIM="\033[2m"
RESET="\033[0m"

PASSED=0
FAILED=0

assert_status() {
  local name="$1"
  local expected="$2"
  local actual="$3"
  local body="$4"

  if [[ "$actual" == "$expected" ]]; then
    echo -e "  ${GREEN}PASS${RESET} $name ${DIM}(HTTP $actual)${RESET}"
    PASSED=$((PASSED + 1))
  else
    echo -e "  ${RED}FAIL${RESET} $name ${DIM}(expected $expected, got $actual)${RESET}"
    echo -e "       ${DIM}$body${RESET}"
    FAILED=$((FAILED + 1))
  fi
}

echo -e "${BOLD}Security test suite${RESET}"
echo -e "Controller: ${CYAN}$CONTROLLER${RESET}"
echo ""

# --- Test 1: Rogue commit not on main should be blocked ---
echo -e "${BOLD}1. Branch enforcement${RESET}"

ROGUE_SHA="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
BODY=$(curl -s -w "\n%{http_code}" -X POST "$CONTROLLER/trigger?app=okbot-go&repo=git@github.com:koreyGambill/okbot.git&github_owner=koreyGambill&github_repo=okbot&github_sha=$ROGUE_SHA" 2>&1)
HTTP=$(echo "$BODY" | tail -1)
RESPONSE=$(echo "$BODY" | sed '$d')
assert_status "rogue commit blocked" "403" "$HTTP" "$RESPONSE"

# --- Test 2: No SHA should be blocked for protected workflows ---
echo -e "${BOLD}2. Missing SHA enforcement${RESET}"

BODY=$(curl -s -w "\n%{http_code}" -X POST "$CONTROLLER/trigger?app=okbot-go&repo=git@github.com:koreyGambill/okbot.git" 2>&1)
HTTP=$(echo "$BODY" | tail -1)
RESPONSE=$(echo "$BODY" | sed '$d')
assert_status "no SHA blocked for protected workflow" "403" "$HTTP" "$RESPONSE"

# --- Test 3: Unprotected workflow should be allowed without SHA ---
echo -e "${BOLD}3. Unprotected workflow allowed${RESET}"

BODY=$(curl -s -w "\n%{http_code}" -X POST "$CONTROLLER/trigger?app=build-test-deploy" 2>&1)
HTTP=$(echo "$BODY" | tail -1)
RESPONSE=$(echo "$BODY" | sed '$d')
assert_status "unprotected workflow allowed" "200" "$HTTP" "$RESPONSE"

# --- Test 4: Forged webhook without signature ---
echo -e "${BOLD}4. Webhook signature verification${RESET}"

PAYLOAD='{"ref":"refs/heads/main","after":"fake123","commits":[],"repository":{"full_name":"koreyGambill/okbot","ssh_url":"git@github.com:koreyGambill/okbot.git"}}'
BODY=$(curl -s -w "\n%{http_code}" -X POST "$CONTROLLER/webhooks/github" \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: push" \
  -d "$PAYLOAD" 2>&1)
HTTP=$(echo "$BODY" | tail -1)
RESPONSE=$(echo "$BODY" | sed '$d')
assert_status "forged webhook rejected" "401" "$HTTP" "$RESPONSE"

# --- Test 5: Rate limiting ---
echo -e "${BOLD}5. Rate limiting${RESET}"

RATE_OK=true
for i in $(seq 1 25); do
  BODY=$(curl -s -w "\n%{http_code}" -X POST "$CONTROLLER/trigger?app=ratelimit-test" 2>&1)
  HTTP=$(echo "$BODY" | tail -1)
  if [[ "$HTTP" == "429" ]]; then
    assert_status "rate limit triggered after $i requests" "429" "$HTTP" ""
    RATE_OK=false
    break
  fi
done
if $RATE_OK; then
  echo -e "  ${RED}FAIL${RESET} rate limit not triggered after 25 requests"
  FAILED=$((FAILED + 1))
fi

# --- Test 6: Audit log populated ---
echo -e "${BOLD}6. Audit logging${RESET}"

BODY=$(curl -s "$CONTROLLER/audit" 2>&1)
COUNT=$(echo "$BODY" | grep -o '"action"' | wc -l | tr -d ' ')
if [[ "$COUNT" -gt 0 ]]; then
  echo -e "  ${GREEN}PASS${RESET} audit log has $COUNT entries"
  PASSED=$((PASSED + 1))
else
  echo -e "  ${RED}FAIL${RESET} audit log empty"
  FAILED=$((FAILED + 1))
fi

# --- Test 7: Cache signature verification ---
echo -e "${BOLD}7. Cache signing${RESET}"

# This is a unit-level check — we verify the signing functions work
# The actual tamper detection requires a running store with SPARKWING_CACHE_SECRET
echo -e "  ${YELLOW}SKIP${RESET} cache signing requires SPARKWING_CACHE_SECRET (unit tests cover this)"

# --- Summary ---
echo ""
TOTAL=$((PASSED + FAILED))
echo -e "${BOLD}Results${RESET}"
echo -e "  ${GREEN}${PASSED} passed${RESET}  ${RED}${FAILED} failed${RESET}  ${DIM}${TOTAL} total${RESET}"

if [[ $FAILED -eq 0 ]]; then
  echo -e "\n${GREEN}${BOLD}All security tests passed.${RESET}"
else
  echo -e "\n${RED}${BOLD}${FAILED} security test(s) failed.${RESET}"
  exit 1
fi
