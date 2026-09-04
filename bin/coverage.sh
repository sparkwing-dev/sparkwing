#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"

CYAN="\033[36m"
GREEN="\033[32m"
RED="\033[31m"
YELLOW="\033[33m"
BOLD="\033[1m"
DIM="\033[2m"
RESET="\033[0m"

echo -e "${BOLD}Test coverage report${RESET}"
echo ""

SPARKWING_DISABLE_FLAKY=1 go test ./... -coverprofile=coverage.out -count=1 2>&1 | \
  grep -E "^ok|^FAIL|coverage:" | while read -r line; do
    if echo "$line" | grep -q "coverage:"; then
      pkg=$(echo "$line" | awk '{print $2}')
      pct=$(echo "$line" | grep -o '[0-9.]*%')
      num=${pct%\%}

      color="$RED"
      if (( $(echo "$num >= 80" | bc -l 2>/dev/null || echo 0) )); then
        color="$GREEN"
      elif (( $(echo "$num >= 50" | bc -l 2>/dev/null || echo 0) )); then
        color="$YELLOW"
      fi

      printf "  %-55s ${color}%s${RESET}\n" "$pkg" "$pct"
    fi
  done

echo ""

echo -e "${BOLD}Per-function coverage:${RESET}"
go tool cover -func=coverage.out 2>&1 | tail -1 | while read -r line; do
  pct=$(echo "$line" | grep -o '[0-9.]*%')
  echo -e "  Total: ${CYAN}${pct}${RESET}"
done

echo ""

go tool cover -html=coverage.out -o coverage.html 2>/dev/null
echo -e "HTML report: ${CYAN}coverage.html${RESET}"
echo -e "Detailed:    ${DIM}go tool cover -func=coverage.out${RESET}"
