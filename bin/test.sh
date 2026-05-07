#!/usr/bin/env bash
# wing: test
# desc: Run Go tests with formatted pass/fail summary
# arg: package (optional, default: ./...) Go package pattern
set -uo pipefail

CYAN="\033[36m"
GREEN="\033[32m"
RED="\033[31m"
YELLOW="\033[33m"
BOLD="\033[1m"
DIM="\033[2m"
RESET="\033[0m"

PACKAGE="${1:-./...}"
FLAKY_RUNS="${FLAKY_RUNS:-0}"

echo -e "${BOLD}sparkwing test suite${RESET}"
echo ""

# Run tests with verbose output, skip the intentional flaky test
START=$SECONDS
OUTPUT=$(SPARKWING_DISABLE_FLAKY=1 go test "$PACKAGE" -count=1 -v 2>&1)
DURATION=$(( SECONDS - START ))
EXIT_CODE=${PIPESTATUS[0]:-$?}

# Parse results
PASSED=0
FAILED=0
SKIPPED=0
FAIL_NAMES=()

while IFS= read -r line; do
  if [[ "$line" =~ "--- PASS:" ]]; then
    PASSED=$((PASSED + 1))
  elif [[ "$line" =~ "--- FAIL:" ]]; then
    FAILED=$((FAILED + 1))
    name=$(echo "$line" | sed 's/.*--- FAIL: \([^ ]*\).*/\1/')
    FAIL_NAMES+=("$name")
  elif [[ "$line" =~ "--- SKIP:" ]]; then
    SKIPPED=$((SKIPPED + 1))
  fi
done <<< "$OUTPUT"

TOTAL=$((PASSED + FAILED + SKIPPED))

# Per-package results
echo -e "${BOLD}Packages${RESET}"
while IFS= read -r line; do
  if [[ "$line" =~ ^ok ]]; then
    pkg=$(echo "$line" | awk '{print $2}')
    dur=$(echo "$line" | awk '{print $3}')
    cov=""
    if [[ "$line" == *"coverage:"* ]]; then
      cov=$(echo "$line" | grep -o '[0-9.]*%')
    fi
    printf "  ${GREEN}PASS${RESET}  %-50s ${DIM}%s${RESET}" "$pkg" "$dur"
    if [[ -n "$cov" ]]; then
      printf "  ${CYAN}%s${RESET}" "$cov"
    fi
    echo ""
  elif [[ "$line" =~ ^FAIL ]]; then
    pkg=$(echo "$line" | awk '{print $2}')
    dur=$(echo "$line" | awk '{print $3}')
    printf "  ${RED}FAIL${RESET}  %-50s ${DIM}%s${RESET}\n" "$pkg" "$dur"
  fi
done <<< "$OUTPUT"

echo ""

# Failed test details
if [[ ${#FAIL_NAMES[@]} -gt 0 ]]; then
  echo -e "${RED}${BOLD}Failed tests:${RESET}"
  for name in "${FAIL_NAMES[@]}"; do
    echo -e "  ${RED}$name${RESET}"
    # Show the failure message
    echo "$OUTPUT" | grep -A2 "--- FAIL: $name" | tail -n +2 | while read -r l; do
      echo -e "    ${DIM}$l${RESET}"
    done
  done
  echo ""
fi

# Summary
echo -e "${BOLD}Results${RESET}"
echo -e "  ${GREEN}${PASSED} passed${RESET}  ${RED}${FAILED} failed${RESET}  ${YELLOW}${SKIPPED} skipped${RESET}  ${DIM}${TOTAL} total${RESET}  ${DIM}${DURATION}s${RESET}"

if [[ $FAILED -eq 0 ]]; then
  echo -e "\n${GREEN}${BOLD}All tests passed.${RESET}"
else
  echo -e "\n${RED}${BOLD}${FAILED} test(s) failed.${RESET}"
fi

# Optional: run flaky detection
if [[ "$FLAKY_RUNS" -gt 0 ]]; then
  echo ""
  echo -e "${BOLD}Flaky detection${RESET} (${FLAKY_RUNS} runs)"
  exec "$0/../bin/flaky-detect.sh" "$FLAKY_RUNS" "$PACKAGE"
fi

exit $EXIT_CODE
