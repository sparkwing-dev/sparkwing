#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CASE_ROOT="$(mktemp -d)"
RUNNER_PID=""

cleanup() {
  if [[ -n "$RUNNER_PID" ]] && kill -0 "$RUNNER_PID" 2>/dev/null; then
    kill -TERM "$RUNNER_PID" 2>/dev/null || true
    wait "$RUNNER_PID" 2>/dev/null || true
  fi
  rm -rf "$CASE_ROOT"
}
trap cleanup EXIT

cat >"$CASE_ROOT/fake-guard" <<'EOF'
#!/usr/bin/env bash
trap 'printf terminated >"$CHAOS_SOAK_TEST_MARKER"; exit 130' TERM INT HUP
printf started >"$CHAOS_SOAK_TEST_STARTED"
while :; do
  sleep 1
done
EOF
chmod +x "$CASE_ROOT/fake-guard"

CHAOS_SOAK_WORKTREE="$CASE_ROOT" \
CHAOS_SOAK_LOGDIR="$CASE_ROOT/logs" \
CHAOS_SOAK_GUARD_BIN="$CASE_ROOT/fake-guard" \
CHAOS_SOAK_TEST_MARKER="$CASE_ROOT/terminated" \
CHAOS_SOAK_TEST_STARTED="$CASE_ROOT/started" \
bash "$ROOT/bin/chaos-soak-runner.sh" >"$CASE_ROOT/runner.log" 2>&1 &
RUNNER_PID=$!

deadline=$((SECONDS + 5))
while [[ ! -f "$CASE_ROOT/started" ]]; do
  if (( SECONDS >= deadline )); then
    echo "chaos-soak-runner-test: guard did not start" >&2
    exit 1
  fi
  sleep 0.05
done

kill -TERM "$RUNNER_PID"
set +e
wait "$RUNNER_PID"
status=$?
set -e
RUNNER_PID=""

if [[ $status -ne 143 ]]; then
  echo "chaos-soak-runner-test: exit=$status, want 143" >&2
  exit 1
fi
if [[ "$(cat "$CASE_ROOT/terminated")" != "terminated" ]]; then
  echo "chaos-soak-runner-test: runner exited before its guard" >&2
  exit 1
fi
