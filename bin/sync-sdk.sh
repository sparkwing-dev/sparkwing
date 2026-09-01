#!/usr/bin/env bash
set -euo pipefail


SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

CYAN="\033[36m"
GREEN="\033[32m"
BOLD="\033[1m"
RESET="\033[0m"

cd "$ROOT"

echo -e "${CYAN}Pushing sparkwing to GitHub...${RESET}"
git push 2>&1 | tail -3

COMMIT=$(git rev-parse HEAD)
SHORT=$(git rev-parse --short HEAD)
TIMESTAMP=$(TZ=UTC git log -1 --format=%cd --date=format:%Y%m%d%H%M%S HEAD)
PSEUDO="v0.0.0-${TIMESTAMP}-${COMMIT:0:12}"

echo ""
echo -e "${GREEN}${BOLD}SDK version:${RESET} ${PSEUDO}"
echo ""
echo -e "${BOLD}go.mod require line:${RESET}"
echo "require github.com/sparkwing-dev/sparkwing ${PSEUDO}"
echo ""
echo -e "${BOLD}To update an app's .sparkwing/go.mod:${RESET}"
echo "  cd <app>/.sparkwing"
echo "  # Update the require line, then:"
echo "  GOPRIVATE=github.com/sparkwing-dev/sparkwing go mod tidy"
