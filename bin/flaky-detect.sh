#!/usr/bin/env bash
# wing: flaky-detect
# desc: Run Go tests N times and report flaky tests with failure rates
# arg: runs (optional, default: 10) Number of test iterations
# arg: package (optional, default: ./...) Go package pattern
set -euo pipefail

RUNS="${1:-10}"
PACKAGE="${2:-./...}"

CYAN="\033[36m"
GREEN="\033[32m"
RED="\033[31m"
YELLOW="\033[33m"
BOLD="\033[1m"
DIM="\033[2m"
RESET="\033[0m"

echo -e "${BOLD}Flaky test detector${RESET}"
echo -e "Running ${CYAN}${RUNS}${RESET} iterations of ${CYAN}${PACKAGE}${RESET}"
echo ""

TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

TOTAL_TESTS=0
declare -A PASS_COUNT
declare -A FAIL_COUNT
declare -A FAIL_MSGS

for i in $(seq 1 "$RUNS"); do
  printf "  run %2d/%d ... " "$i" "$RUNS"

  # Run tests, capture output
  OUTPUT="$TMPDIR/run-$i.txt"
  if go test "$PACKAGE" -count=1 -v > "$OUTPUT" 2>&1; then
    echo -e "${GREEN}pass${RESET}"
  else
    echo -e "${RED}fail${RESET}"
  fi

  # Parse results
  while IFS= read -r line; do
    if [[ "$line" =~ ^"--- PASS: " ]]; then
      TEST=$(echo "$line" | sed 's/--- PASS: \([^ ]*\).*/\1/')
      PASS_COUNT[$TEST]=$(( ${PASS_COUNT[$TEST]:-0} + 1 ))
      TOTAL_TESTS=1
    elif [[ "$line" =~ ^"--- FAIL: " ]]; then
      TEST=$(echo "$line" | sed 's/--- FAIL: \([^ ]*\).*/\1/')
      FAIL_COUNT[$TEST]=$(( ${FAIL_COUNT[$TEST]:-0} + 1 ))
      FAIL_MSGS[$TEST]="${FAIL_MSGS[$TEST]:-}run $i; "
      TOTAL_TESTS=1
    fi
  done < "$OUTPUT"
done

echo ""

# Collect all test names
declare -A ALL_TESTS
for t in "${!PASS_COUNT[@]}"; do ALL_TESTS[$t]=1; done
for t in "${!FAIL_COUNT[@]}"; do ALL_TESTS[$t]=1; done

# Categorize
FLAKY=()
ALWAYS_FAIL=()
ALWAYS_PASS=()

for t in "${!ALL_TESTS[@]}"; do
  passes=${PASS_COUNT[$t]:-0}
  fails=${FAIL_COUNT[$t]:-0}
  total=$((passes + fails))

  if [[ $fails -gt 0 && $passes -gt 0 ]]; then
    FLAKY+=("$t")
  elif [[ $fails -gt 0 && $passes -eq 0 ]]; then
    ALWAYS_FAIL+=("$t")
  else
    ALWAYS_PASS+=("$t")
  fi
done

# Report
if [[ ${#FLAKY[@]} -gt 0 ]]; then
  echo -e "${YELLOW}${BOLD}Flaky tests (${#FLAKY[@]})${RESET}"
  for t in "${FLAKY[@]}"; do
    passes=${PASS_COUNT[$t]:-0}
    fails=${FAIL_COUNT[$t]:-0}
    total=$((passes + fails))
    rate=$((passes * 100 / total))
    echo -e "  ${YELLOW}${t}${RESET}  ${GREEN}${passes}/${total} pass${RESET} (${rate}%)"
  done
  echo ""
fi

if [[ ${#ALWAYS_FAIL[@]} -gt 0 ]]; then
  echo -e "${RED}${BOLD}Always failing (${#ALWAYS_FAIL[@]})${RESET}"
  for t in "${ALWAYS_FAIL[@]}"; do
    echo -e "  ${RED}${t}${RESET}  0/${RUNS} pass"
  done
  echo ""
fi

echo -e "${GREEN}${BOLD}Stable tests: ${#ALWAYS_PASS[@]}${RESET}"
echo -e "${DIM}Total unique tests seen: ${#ALL_TESTS[@]}${RESET}"

# Write JSON report for dashboard
JSON_OUT="${SPARKWING_FLAKY_REPORT:-flaky-report.json}"
{
  echo '{"runs":'$RUNS',"timestamp":"'$(date -u +%Y-%m-%dT%H:%M:%SZ)'",'
  echo '"flaky":['
  sep=""
  for t in "${FLAKY[@]}"; do
    passes=${PASS_COUNT[$t]:-0}
    fails=${FAIL_COUNT[$t]:-0}
    total=$((passes + fails))
    rate=$((passes * 100 / total))
    echo "${sep}{\"name\":\"$t\",\"passes\":$passes,\"fails\":$fails,\"total\":$total,\"rate\":$rate}"
    sep=","
  done
  echo '],"always_failing":['
  sep=""
  for t in "${ALWAYS_FAIL[@]}"; do
    echo "${sep}{\"name\":\"$t\",\"total\":$RUNS}"
    sep=","
  done
  echo '],"stable_count":'${#ALWAYS_PASS[@]}',"total_tests":'${#ALL_TESTS[@]}'}'
} > "$JSON_OUT"
echo -e "${DIM}Report written to ${JSON_OUT}${RESET}"

if [[ ${#FLAKY[@]} -gt 0 ]]; then
  exit 1
fi
