#!/usr/bin/env bash
# wing: port-forward
# desc: Set up kubectl port-forwards for all sparkwing services
set -euo pipefail

CYAN="\033[36m"
GREEN="\033[32m"
DIM="\033[2m"
BOLD="\033[1m"
RESET="\033[0m"

FORWARDS=(
  # Sparkwing core: 9000-9009
  "9000:sparkwing:svc/sparkwing-lite:80"
  "9001:sparkwing:svc/sparkwing-controller:80"
  "9002:sparkwing:svc/sparkwing-web:3100"
  "9003:sparkwing:svc/dind:2375"
  "9004:registry:svc/registry:5000"

  # Okbot apps: 9050+
  "9050:okbot:deploy/okbot-go:8080"
  "9051:okbot:deploy/okbot-python:8080"
  "9052:okbot:deploy/okbot-java:8080"
  "9053:okbot:deploy/okbot-node:8080"
  "9054:okbot:deploy/okbot-ruby:8080"
  "9055:okbot:deploy/okbot-rust:8080"
  "9056:okbot:deploy/okbot-elixir:8080"
  "9057:okbot:deploy/okbot-react:8080"
  "9058:okbot:deploy/okbot-nextjs:8080"
)

# Collect all ports and kill existing forwards
PORTS=""
for entry in "${FORWARDS[@]}"; do
  port="${entry%%:*}"
  PORTS="${PORTS:+$PORTS,}:$port"
  pid=$(lsof -ti :"$port" 2>/dev/null || true)
  if [[ -n "$pid" ]]; then
    kill $pid 2>/dev/null || true
  fi
done

sleep 1

echo -e "${BOLD}Sparkwing core${RESET}"
for entry in "${FORWARDS[@]}"; do
  IFS=: read -r port ns resource svc_port <<< "$entry"
  # Skip apps that aren't running yet
  kubectl port-forward -n "$ns" "$resource" "$port:$svc_port" &>/dev/null &
  echo -e "  ${CYAN}:${GREEN}${BOLD}${port}${RESET} → ${ns}/${resource}"
  # Print section header when switching from core to apps
  if [[ "$port" == "9003" ]]; then
    echo -e "${BOLD}Okbot apps${RESET}"
  fi
done

echo ""
echo -e "${BOLD}Port forwards running.${RESET}"
echo -e "${DIM}Kill: kill \$(lsof -ti ${PORTS})${RESET}"
