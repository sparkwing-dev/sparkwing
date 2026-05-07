#!/usr/bin/env bash
# wing: verify-replay
# desc: Quick verification of TOD-077 replay primitives. Runs the focused
#       unit tests against the orchestrator + store packages so the
#       substrate + lineage stamping are exercised against real SQLite.
#       Use after touching pkg/orchestrator/replay.go or the runs schema.
# arg:  none

set -euo pipefail

GREEN="\033[32m"
RED="\033[31m"
RESET="\033[0m"

pass() { echo -e "  ${GREEN}PASS${RESET} $1"; }
fail() { echo -e "  ${RED}FAIL${RESET} $1"; exit 1; }

cd "$(git rev-parse --show-toplevel)"

echo "==> dispatch substrate (TOD-077 PR 1)"
go test ./pkg/orchestrator/store/ -run TestDispatch -count=1 >/dev/null 2>&1 || fail "store dispatch tests"
pass "store dispatch round-trip + cap + cascade"

echo "==> orchestrator dispatch hook (TOD-077 PR 1)"
go test ./pkg/orchestrator/ -run TestDispatchSnapshot -count=1 >/dev/null 2>&1 || fail "dispatch snapshot tests"
pass "snapshot capture + masker + best-effort"

echo "==> replay primitives (TOD-077 PR 3)"
go test ./pkg/orchestrator/ -run 'TestEnvelopeTruncated|TestMintReplayRun|TestRunReplayNode' -count=1 >/dev/null 2>&1 || fail "replay tests"
pass "mint replay run + code-drift + envelope truncation"

echo
echo "verify-replay: ok"
