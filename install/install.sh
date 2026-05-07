#!/usr/bin/env bash
#
# sparkwing-runner installer for macOS (launchd) and Linux (systemd user).
#
# Usage (interactive):
#   bash install/install.sh
#
# Usage (non-interactive, e.g. for scripting or team onboarding docs):
#   SPARKWING_CONTROLLER=https://api-sparkwing.rangz.dev \
#   SPARKWING_LOGS=https://logs-sparkwing.rangz.dev \
#   SPARKWING_API_TOKEN=... \
#   RUNNER_NAME=laptop-korey \
#   MAX_CONCURRENT=2 \
#   bash install/install.sh --yes
#
# What it does:
#   1. Confirms sparkwing-runner is on your PATH (or tells you how to install it)
#   2. Creates a config directory (~/.sparkwing/ by default)
#   3. Renders a launchd plist (macOS) or a systemd user unit (Linux) from
#      the template, with your controller URL, token, and name baked in
#   4. Loads the service so the runner starts at login / boot
#   5. Prints instructions for pause / stop / uninstall
#
# This script does NOT build sparkwing-runner itself. Install it first:
#   go install github.com/sparkwing-dev/sparkwing/cmd/sparkwing-runner@latest
# or build from source and put the binary on your PATH.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ---------- helpers ----------

log()  { printf "\033[36m==>\033[0m %s\n" "$*"; }
warn() { printf "\033[33m==>\033[0m %s\n" "$*" >&2; }
err()  { printf "\033[31m==>\033[0m %s\n" "$*" >&2; exit 1; }

ask() {
  local prompt="$1"
  local default="${2:-}"
  local answer
  if [ -n "$default" ]; then
    read -p "$prompt [$default]: " answer || true
    echo "${answer:-$default}"
  else
    read -p "$prompt: " answer || true
    echo "$answer"
  fi
}

ask_secret() {
  local prompt="$1"
  local answer
  read -s -p "$prompt: " answer || true
  echo
  echo "$answer"
}

detect_platform() {
  case "$(uname -s)" in
    Darwin) echo "macos" ;;
    Linux)  echo "linux" ;;
    MINGW*|MSYS*|CYGWIN*)
      err "This installer is for the Linux/macOS sparkwing-runner Service.

The cluster-mode runner is not supported on Windows. For the Windows CLI
(\`sparkwing.exe\`), run the public installer in this same Git Bash:

  curl -fsSL https://sparkwing.dev/install.sh | bash

Windows CLI users dispatch pipelines to Linux/macOS runners or to a
remote cluster -- there's no local runner Service to install." ;;
    *) err "unsupported platform: $(uname -s). Supported: Darwin (macOS), Linux." ;;
  esac
}

# ---------- pre-flight ----------

NON_INTERACTIVE=false
if [[ "${1:-}" == "--yes" || "${1:-}" == "-y" ]]; then
  NON_INTERACTIVE=true
fi

BINARY_PATH="$(command -v sparkwing-runner || true)"
if [ -z "$BINARY_PATH" ]; then
  err "sparkwing-runner binary not found on PATH.

Install it with one of:
  go install github.com/sparkwing-dev/sparkwing/cmd/sparkwing-runner@latest
  (then ensure \$GOPATH/bin or ~/go/bin is on your PATH)

Or build from source and place the binary on your PATH."
fi
log "found binary: $BINARY_PATH"

PLATFORM="$(detect_platform)"
log "detected platform: $PLATFORM"

if ! command -v docker >/dev/null 2>&1; then
  warn "docker not found on PATH. Most sparkwing jobs need Docker — install Docker Desktop / colima / rancher-desktop before running real work."
fi

# ---------- collect config ----------

if [ "$NON_INTERACTIVE" = false ]; then
  log "configuring sparkwing-runner. Press enter to accept defaults."
  echo
fi

CONTROLLER_URL="${SPARKWING_CONTROLLER:-}"
LOGS_URL="${SPARKWING_LOGS:-}"
API_TOKEN="${SPARKWING_API_TOKEN:-}"
RUNNER_NAME="${RUNNER_NAME:-}"
MAX_CONCURRENT="${MAX_CONCURRENT:-}"

if [ -z "$CONTROLLER_URL" ]; then
  CONTROLLER_URL="$(ask 'Controller URL' 'https://api-sparkwing.rangz.dev')"
fi
if [ -z "$LOGS_URL" ]; then
  LOGS_URL="$(ask 'Logs service URL' 'https://logs-sparkwing.rangz.dev')"
fi
if [ -z "$API_TOKEN" ]; then
  API_TOKEN="$(ask_secret 'API token (will not be echoed)')"
fi
if [ -z "$API_TOKEN" ]; then
  err "API token is required. Get one from your team's sparkwing admin."
fi
if [ -z "$RUNNER_NAME" ]; then
  DEFAULT_NAME="$(hostname -s | tr '[:upper:]' '[:lower:]')-runner"
  RUNNER_NAME="$(ask 'Runner name (shown in dashboard)' "$DEFAULT_NAME")"
fi
if [ -z "$MAX_CONCURRENT" ]; then
  MAX_CONCURRENT="$(ask 'Max concurrent jobs' '2')"
fi

log ""
log "config summary:"
log "  binary:         $BINARY_PATH"
log "  controller:     $CONTROLLER_URL"
log "  logs:           $LOGS_URL"
log "  runner name:    $RUNNER_NAME"
log "  max concurrent: $MAX_CONCURRENT"
log ""

if [ "$NON_INTERACTIVE" = false ]; then
  read -p "Install with these settings? [y/N] " confirm
  if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
    err "aborted by user"
  fi
fi

# ---------- render and install ----------

SPARKWING_HOME="${HOME}/.sparkwing"
LOG_PATH="${SPARKWING_HOME}/runner.log"
mkdir -p "$SPARKWING_HOME"

if [ "$PLATFORM" = "macos" ]; then
  TEMPLATE="${SCRIPT_DIR}/macos/com.sparkwing.runner.plist.template"
  [ -f "$TEMPLATE" ] || err "missing template: $TEMPLATE"

  PLIST_DIR="${HOME}/Library/LaunchAgents"
  PLIST_PATH="${PLIST_DIR}/com.sparkwing.runner.plist"
  mkdir -p "$PLIST_DIR"

  # Unload if already installed so we can replace cleanly.
  if launchctl list com.sparkwing.runner >/dev/null 2>&1; then
    log "unloading existing LaunchAgent..."
    launchctl unload "$PLIST_PATH" 2>/dev/null || true
  fi

  sed \
    -e "s|__BINARY_PATH__|${BINARY_PATH}|g" \
    -e "s|__RUNNER_NAME__|${RUNNER_NAME}|g" \
    -e "s|__CONTROLLER_URL__|${CONTROLLER_URL}|g" \
    -e "s|__LOGS_URL__|${LOGS_URL}|g" \
    -e "s|__API_TOKEN__|${API_TOKEN}|g" \
    -e "s|__MAX_CONCURRENT__|${MAX_CONCURRENT}|g" \
    -e "s|__HOME__|${HOME}|g" \
    -e "s|__LOG_PATH__|${LOG_PATH}|g" \
    "$TEMPLATE" > "$PLIST_PATH"

  chmod 600 "$PLIST_PATH"  # contains the API token
  log "wrote $PLIST_PATH (mode 600)"

  log "loading LaunchAgent..."
  launchctl load "$PLIST_PATH"

  log ""
  log "sparkwing-runner is now running as a LaunchAgent."
  log ""
  log "Useful commands:"
  log "  tail -f $LOG_PATH                                # view runner logs"
  log "  launchctl list | grep sparkwing                  # see running state"
  log "  launchctl unload ~/Library/LaunchAgents/com.sparkwing.runner.plist   # pause"
  log "  launchctl load   ~/Library/LaunchAgents/com.sparkwing.runner.plist   # resume"
  log "  rm ~/Library/LaunchAgents/com.sparkwing.runner.plist                 # uninstall (after unload)"

elif [ "$PLATFORM" = "linux" ]; then
  TEMPLATE="${SCRIPT_DIR}/linux/sparkwing-runner.service.template"
  [ -f "$TEMPLATE" ] || err "missing template: $TEMPLATE"

  UNIT_DIR="${HOME}/.config/systemd/user"
  UNIT_PATH="${UNIT_DIR}/sparkwing-runner.service"
  mkdir -p "$UNIT_DIR"

  sed \
    -e "s|__BINARY_PATH__|${BINARY_PATH}|g" \
    -e "s|__RUNNER_NAME__|${RUNNER_NAME}|g" \
    -e "s|__CONTROLLER_URL__|${CONTROLLER_URL}|g" \
    -e "s|__LOGS_URL__|${LOGS_URL}|g" \
    -e "s|__API_TOKEN__|${API_TOKEN}|g" \
    -e "s|__MAX_CONCURRENT__|${MAX_CONCURRENT}|g" \
    "$TEMPLATE" > "$UNIT_PATH"

  chmod 600 "$UNIT_PATH"  # contains the API token
  log "wrote $UNIT_PATH (mode 600)"

  log "reloading systemd user units..."
  systemctl --user daemon-reload

  log "enabling and starting sparkwing-runner..."
  systemctl --user enable --now sparkwing-runner

  log ""
  log "sparkwing-runner is now running as a systemd user service."
  log ""
  log "Useful commands:"
  log "  journalctl --user -u sparkwing-runner -f             # view runner logs"
  log "  systemctl --user status sparkwing-runner             # see running state"
  log "  systemctl --user stop sparkwing-runner               # pause"
  log "  systemctl --user start sparkwing-runner              # resume"
  log "  systemctl --user disable --now sparkwing-runner      # uninstall"
  log "  rm ~/.config/systemd/user/sparkwing-runner.service   # remove unit file"
  log ""
  log "Note: if you want the runner to keep running when you log out,"
  log "enable lingering with 'loginctl enable-linger \$USER' as root."
fi

log ""
log "done! The runner will now contribute idle capacity to your team's controller."
