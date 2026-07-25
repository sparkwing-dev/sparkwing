#!/usr/bin/env bash
# sparkwing run: flaky-detect
# desc: Run Go tests N times and report flaky tests with failure rates
# arg: runs (optional, default: 10) Number of test iterations
# arg: package (optional, default: ./...) Go package pattern
# arg: load (optional, default: 1) Concurrent copies per iteration
set -euo pipefail

RUNS="${1:-10}"
PACKAGE="${2:-./...}"
# Hunting a scheduling race means raising this past the CPU count, but it stays
# opt-in: packages bound by a shared resource rather than by CPU, such as the
# docker-backed service suites, fail under any concurrency, and a detector that
# cries wolf on them is one nobody reads.
LOAD="${3:-1}"

if [[ ! "$LOAD" =~ ^[1-9][0-9]*$ ]]; then
  echo "flaky-detect: load must be a positive integer, got '$LOAD'" >&2
  exit 2
fi

# Each iteration produces LOAD results per test, so every denominator below
# counts observations rather than iterations; the two only coincide at load 1.
OBSERVATIONS=$((RUNS * LOAD))

CYAN="\033[36m"
GREEN="\033[32m"
RED="\033[31m"
YELLOW="\033[33m"
BOLD="\033[1m"
DIM="\033[2m"
RESET="\033[0m"

echo -e "${BOLD}Flaky test detector${RESET}"
echo -e "Running ${CYAN}${RUNS}${RESET} iterations of ${CYAN}${PACKAGE}${RESET}, ${CYAN}${LOAD}${RESET} at a time"
echo ""

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

TOTAL_TESTS=0
declare -A PASS_COUNT
declare -A FAIL_COUNT
declare -A FAIL_RUNS

for i in $(seq 1 "$RUNS"); do
  printf "  run %2d/%d ... " "$i" "$RUNS"

  # Run LOAD copies at once. The flakes worth catching are races, not
  # repeats: a serial rerun of one package leaves the box idle, so reader
  # goroutines never starve and short leases never straddle a slow write.
  # Concurrent copies put the package under what `go test ./...` does to it.
  PIDS=()
  OUTPUTS=()
  for c in $(seq 1 "$LOAD"); do
    OUTPUT="$TMPDIR/run-$i-$c.txt"
    OUTPUTS+=("$OUTPUT")
    go test "$PACKAGE" -count=1 -v > "$OUTPUT" 2>&1 &
    PIDS+=("$!")
  done

  ITER_STATUS=0
  for pid in "${PIDS[@]}"; do
    wait "$pid" || ITER_STATUS=1
  done
  if [[ $ITER_STATUS -eq 0 ]]; then
    echo -e "${GREEN}pass${RESET}"
  else
    echo -e "${RED}fail${RESET}"
  fi

  # Parse results. Counts are per observation, but the run list is per
  # iteration: a test that fails in every concurrent copy still failed run $i
  # once, so the copies are pooled here before the run number is recorded.
  declare -A ITER_FAILED=()
  for OUTPUT in "${OUTPUTS[@]}"; do
    while IFS= read -r line; do
      if [[ "$line" =~ ^"--- PASS: " ]]; then
        TEST=$(echo "$line" | sed 's/--- PASS: \([^ ]*\).*/\1/')
        PASS_COUNT[$TEST]=$(( ${PASS_COUNT[$TEST]:-0} + 1 ))
        TOTAL_TESTS=1
      elif [[ "$line" =~ ^"--- FAIL: " ]]; then
        TEST=$(echo "$line" | sed 's/--- FAIL: \([^ ]*\).*/\1/')
        FAIL_COUNT[$TEST]=$(( ${FAIL_COUNT[$TEST]:-0} + 1 ))
        ITER_FAILED[$TEST]=1
        TOTAL_TESTS=1
      fi
    done < "$OUTPUT"
  done
  for t in "${!ITER_FAILED[@]}"; do
    FAIL_RUNS[$t]="${FAIL_RUNS[$t]:-}$i "
  done
done

echo ""

# Collect all test names
# Initialised empty rather than merely declared: under `set -u` an associative
# array with no elements is still unset, so ${#ALL_TESTS[@]} aborts the report
# whenever a run produced no test lines at all -- a package with no tests, or
# one that failed to compile.
declare -A ALL_TESTS=()
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
    echo -e "    ${DIM}failed in runs: ${FAIL_RUNS[$t]% }${RESET}"
  done
  echo ""
fi

if [[ ${#ALWAYS_FAIL[@]} -gt 0 ]]; then
  echo -e "${RED}${BOLD}Always failing (${#ALWAYS_FAIL[@]})${RESET}"
  for t in "${ALWAYS_FAIL[@]}"; do
    echo -e "  ${RED}${t}${RESET}  0/${OBSERVATIONS} pass"
  done
  echo ""
fi

echo -e "${GREEN}${BOLD}Stable tests: ${#ALWAYS_PASS[@]}${RESET}"
echo -e "${DIM}Total unique tests seen: ${#ALL_TESTS[@]}${RESET}"

# Write JSON report for dashboard
JSON_OUT="${SPARKWING_FLAKY_REPORT:-flaky-report.json}"
timestamp="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
{
  echo "{\"runs\":$RUNS,\"load\":$LOAD,\"observations\":$OBSERVATIONS,\"timestamp\":\"$timestamp\","
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
    echo "${sep}{\"name\":\"$t\",\"total\":$OBSERVATIONS}"
    sep=","
  done
  echo '],"stable_count":'${#ALWAYS_PASS[@]}',"total_tests":'${#ALL_TESTS[@]}'}'
} > "$JSON_OUT"
echo -e "${DIM}Report written to ${JSON_OUT}${RESET}"

if [[ ${#FLAKY[@]} -gt 0 ]]; then
  exit 1
fi
