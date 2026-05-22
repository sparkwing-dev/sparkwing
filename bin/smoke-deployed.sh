#!/usr/bin/env bash
# End-to-end smoke test for Mode 4 (laptop -> hosted controller)
# against the deployed kikd-prod cluster.
#
# Usage:
#   bash bin/smoke-deployed.sh
#   bash bin/smoke-deployed.sh --profile <name>     # override profile (default: prod)
#   bash bin/smoke-deployed.sh --keep               # leave run state in $RUN_DIR
#
# What it checks:
#   1. The configured profile's controller responds on /api/v1/health.
#   2. The profile's token authenticates against /api/v1/runs.
#   3. `sparkwing run weather-report` against a backends.yaml that
#      routes state.type=controller via the profile creates a new
#      run on the cluster (run count increases by exactly one).
#   4. The new run's state is reachable through the controller API
#      and ends in status=success.

set -uo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
PROFILE=prod
KEEP=0

for arg in "$@"; do
  case "$arg" in
    --profile=*) PROFILE="${arg#--profile=}" ;;
    --profile)   shift; PROFILE="${1:-prod}" ;;
    --keep)      KEEP=1 ;;
    -h|--help)
      sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) ;;
  esac
done

RUN_DIR="/tmp/sparkwing-smoke-deployed"
HOME_DIR="$RUN_DIR/home"
CONFIG_PATH="$RUN_DIR/backends.yaml"
LOG_DIR="$RUN_DIR/logs"

mkdir -p "$HOME_DIR" "$LOG_DIR"

log()  { printf "\033[1;34m==>\033[0m %s\n" "$*"; }
ok()   { printf "  \033[1;32mok\033[0m %s\n" "$*"; }
fail() { printf "  \033[1;31mFAIL\033[0m %s\n" "$*"; exit 1; }

teardown() {
  if [ "$KEEP" = "1" ]; then
    log "Run dir kept at $RUN_DIR"
    return
  fi
  rm -rf "$RUN_DIR"
}
trap teardown EXIT

# ---------- pull profile details ----------
log "Reading profile $PROFILE from ~/.config/sparkwing/profiles.yaml"
PROFILES_FILE="$HOME/.config/sparkwing/profiles.yaml"
[ -f "$PROFILES_FILE" ] || fail "profiles file not found at $PROFILES_FILE"

# Simple YAML extractor: the profile's keys are at exactly 4-space
# indent under "  <profile>:". Bail out of the block on the next
# top-level or second-level key. Avoids the yaml/yq dependency.
read_profile_field() {
  local field="$1"
  awk -v profile="$PROFILE" -v field="$field" '
    /^profiles:/ { in_profiles=1; next }
    in_profiles && $0 ~ "^  " profile ":" { in_block=1; next }
    in_block && /^[a-zA-Z]/    { exit }
    in_block && /^  [a-zA-Z]/  { exit }
    in_block {
      sub(/^[[:space:]]*/, "")
      n = index($0, ":")
      if (n == 0) next
      key = substr($0, 1, n-1)
      val = substr($0, n+1)
      sub(/^[[:space:]]+/, "", val)
      sub(/[[:space:]]+$/, "", val)
      if (key == field) { print val; exit }
    }
  ' "$PROFILES_FILE"
}

CONTROLLER_URL=$(read_profile_field controller)
TOKEN=$(read_profile_field token)

[ -n "$CONTROLLER_URL" ] || fail "profile $PROFILE missing controller URL"
[ -n "$TOKEN" ]          || fail "profile $PROFILE missing token"
ok "controller: $CONTROLLER_URL"

# ---------- Phase 1: ingress + auth ----------
log "Phase 1: controller reachable + token authenticates"

health_code=$(curl -fsS -o /dev/null -w "%{http_code}" "$CONTROLLER_URL/api/v1/health")
[ "$health_code" = "200" ] || fail "/api/v1/health returned $health_code"
ok "/api/v1/health = 200"

if ! curl -fsS -H "Authorization: Bearer $TOKEN" "$CONTROLLER_URL/api/v1/runs" >"$LOG_DIR/runs-before.json" 2>&1; then
  fail "/api/v1/runs failed with token (see $LOG_DIR/runs-before.json)"
fi
RUNS_BEFORE=$(python3 -c "
import json
print(len(json.load(open('$LOG_DIR/runs-before.json')).get('runs', [])))
")
ok "/api/v1/runs returned $RUNS_BEFORE existing runs"

# ---------- Phase 2: Mode 4 backends.yaml ----------
log "Phase 2: writing Mode 4 backends.yaml"
cat >"$CONFIG_PATH" <<EOF
defaults:
  state:
    type: controller
    controller: $PROFILE
  cache:
    type: controller
    controller: $PROFILE
  logs:
    type: controller
    controller: $PROFILE
EOF
ok "wrote $CONFIG_PATH"

# ---------- Phase 3: run weather-report through Mode 4 ----------
log "Phase 3: running weather-report against $CONTROLLER_URL"
export SPARKWING_HOME="$HOME_DIR"
export SPARKWING_BACKENDS_CONFIG="$CONFIG_PATH"
if ! (cd "$REPO" && sparkwing run weather-report) >"$LOG_DIR/run.log" 2>&1; then
  tail -40 "$LOG_DIR/run.log" >&2
  fail "sparkwing run weather-report failed (see $LOG_DIR/run.log)"
fi
ok "weather-report completed"

RUN_ID=$(grep -oE 'run_id":"[^"]+' "$LOG_DIR/run.log" | head -1 | cut -d'"' -f3)
[ -n "$RUN_ID" ] || fail "could not extract run_id from $LOG_DIR/run.log"
ok "captured run_id=$RUN_ID"

# ---------- Phase 4: verify the run landed on the controller ----------
log "Phase 4: confirming run $RUN_ID landed on the controller"

if ! curl -fsS -H "Authorization: Bearer $TOKEN" "$CONTROLLER_URL/api/v1/runs/$RUN_ID" >"$LOG_DIR/run.json" 2>&1; then
  fail "controller did not have run $RUN_ID (see $LOG_DIR/run.json)"
fi

STATUS=$(python3 -c "
import json
print(json.load(open('$LOG_DIR/run.json')).get('status', ''))
")
[ "$STATUS" = "success" ] || fail "run status = $STATUS, expected success"
ok "controller reports run $RUN_ID status=success"

if ! curl -fsS -H "Authorization: Bearer $TOKEN" "$CONTROLLER_URL/api/v1/runs" >"$LOG_DIR/runs-after.json" 2>&1; then
  fail "could not re-list runs"
fi
RUNS_AFTER=$(python3 -c "
import json
print(len(json.load(open('$LOG_DIR/runs-after.json')).get('runs', [])))
")
delta=$((RUNS_AFTER - RUNS_BEFORE))
[ "$delta" -ge 1 ] || fail "expected run count to grow by >=1, got delta=$delta (before=$RUNS_BEFORE after=$RUNS_AFTER)"
ok "controller run count grew by $delta ($RUNS_BEFORE -> $RUNS_AFTER)"

log "Mode 4 smoke passed"
echo
echo "  Run dir:     $RUN_DIR"
echo "  Run ID:      $RUN_ID"
echo "  Controller:  $CONTROLLER_URL"
echo "  Dashboard:   ${CONTROLLER_URL/api-/}"
