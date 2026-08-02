#!/usr/bin/env bash
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORKTREE="${CHAOS_SOAK_WORKTREE:-$ROOT}"
REMOTE="${CHAOS_SOAK_REMOTE:-origin}"
LOGDIR="${CHAOS_SOAK_LOGDIR:-$HOME/.cache/sparkwing/chaos-soak}"
SOAK="${SPARKWING_CHAOS_SOAK:-30m}"
GO_TEST_TIMEOUT="${CHAOS_SOAK_TIMEOUT:-45m}"
GUARD_BIN="${CHAOS_SOAK_GUARD_BIN:-}"
GUARD_PID=""
OWN_GUARD_BIN=""

cleanup_guard() {
  if [[ -n "$GUARD_PID" ]] && kill -0 "$GUARD_PID" 2>/dev/null; then
    kill -TERM "$GUARD_PID" 2>/dev/null || true
    while kill -0 "$GUARD_PID" 2>/dev/null; do
      wait "$GUARD_PID" 2>/dev/null || true
    done
  fi
  GUARD_PID=""
}

cleanup() {
  local rc="$1"
  trap - EXIT INT TERM HUP
  cleanup_guard
  if [[ -n "$OWN_GUARD_BIN" ]]; then
    rm -f "$OWN_GUARD_BIN"
  fi
  exit "$rc"
}

trap 'cleanup 130' INT
trap 'cleanup 143' TERM
trap 'cleanup 129' HUP
trap 'cleanup $?' EXIT

mkdir -p "$LOGDIR"
STAMP="$(date +%Y%m%d-%H%M%S)"
SEED="${SPARKWING_CHAOS_SEED:-$(date +%Y%m%d)}"
LOG="$LOGDIR/soak-$STAMP.log"

echo "chaos-soak: worktree=$WORKTREE soak=$SOAK seed=$SEED log=$LOG"

if [[ "${CHAOS_SOAK_SYNC:-}" == "1" ]]; then
  git -C "$WORKTREE" fetch "$REMOTE" main --quiet || { echo "chaos-soak: fetch failed"; exit 2; }
  git -C "$WORKTREE" reset --hard "$REMOTE/main" --quiet || { echo "chaos-soak: reset failed"; exit 2; }
fi

if [[ -z "$GUARD_BIN" ]]; then
  OWN_GUARD_BIN="$(mktemp "$LOGDIR/soakguard.XXXXXX")"
  go -C "$WORKTREE" build -o "$OWN_GUARD_BIN" ./internal/chaos/soakguard || exit 2
  GUARD_BIN="$OWN_GUARD_BIN"
fi

export SPARKWING_CHAOS_SOAK="$SOAK"
export SPARKWING_CHAOS_SEED="$SEED"
"$GUARD_BIN" go -C "$WORKTREE" test -run TestChaos_Soak ./internal/chaos -count=1 -timeout "$GO_TEST_TIMEOUT" >"$LOG" 2>&1 &
GUARD_PID=$!
wait "$GUARD_PID"
RC=$?
GUARD_PID=""

if [[ "$RC" -ne 0 ]]; then
  journal="$(grep -oE 'journal=[^ ]+' "$LOG" | head -1 | cut -d= -f2-)"
  echo "chaos-soak: failed exit=$RC seed=$SEED journal=${journal:-unknown} log=$LOG"
  exit "$RC"
fi

echo "chaos-soak: passed seed=$SEED"
exit 0
