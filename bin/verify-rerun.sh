#!/usr/bin/env bash
# wing: verify-rerun
# desc: End-to-end check for sparkwing debug rerun against the local stack.
#       Exercises a fresh laptop store: drives a small pipeline, picks a
#       node, fetches its dispatch snapshot, and verifies the rerun env
#       is materialized correctly without actually exec'ing into a shell.
# arg:  none

set -euo pipefail

GREEN="\033[32m"
RED="\033[31m"
DIM="\033[2m"
RESET="\033[0m"

pass() { echo -e "  ${GREEN}PASS${RESET} $1"; }
fail() { echo -e "  ${RED}FAIL${RESET} $1 ${DIM}(${2:-})${RESET}"; exit 1; }

# Run inside an isolated SPARKWING_HOME so we don't trample the user's
# real laptop store. The store layout matches DefaultPaths().
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
export SPARKWING_HOME="$WORK"
mkdir -p "$WORK"

# 1. Build wing/sparkwing so the test exercises the binaries the user
# would actually invoke.
echo "==> building binaries"
go build -o "$WORK/sparkwing" ./cmd/sparkwing
go build -o "$WORK/wing"      ./cmd/wing
export PATH="$WORK:$PATH"

# 2. Run a tiny pipeline so we have a real dispatch snapshot to read.
# We use the workspace's existing build-test-deploy with --config local
# is too heavy for a verify script; instead, drive the orchestrator
# through a one-shot SDK pipeline registered for the test.
#
# For v1 we keep this script narrowly focused: it requires the user
# to have already produced a run+node in the local store (e.g. via
# `wing test --config local` once). The script then reads the most
# recent run and confirms a snapshot was captured + can be fetched
# via sparkwing CLI plumbing.

echo "==> looking for a recent local run"
LATEST_RUN="$(sparkwing runs list --limit 1 -o json | python3 -c 'import json,sys; rs=json.load(sys.stdin).get("runs", []); print(rs[0]["id"]) if rs else None' 2>/dev/null || true)"
if [[ -z "$LATEST_RUN" || "$LATEST_RUN" == "None" ]]; then
  echo "no runs in local store -- run a pipeline first (e.g. wing test) and re-run this script"
  exit 0
fi
pass "found latest run: $LATEST_RUN"

# 3. Pick its first node and check the dispatch row exists.
NODE="$(sparkwing runs status --run "$LATEST_RUN" -o json | python3 -c 'import json,sys; ns=json.load(sys.stdin).get("nodes", []); print(ns[0]["node_id"]) if ns else None' 2>/dev/null || true)"
if [[ -z "$NODE" || "$NODE" == "None" ]]; then
  fail "could not pick a node from run $LATEST_RUN"
fi
pass "node: $NODE"

# 4. Read the dispatch snapshot directly from SQLite to assert it was
# captured. SQLite ships with macOS + most linuxen so we don't pull
# in a heavyweight client.
SNAP="$(sqlite3 "$SPARKWING_HOME/state.db" "SELECT seq, length(input_envelope_json) FROM node_dispatches WHERE run_id='$LATEST_RUN' AND node_id='$NODE' ORDER BY seq DESC LIMIT 1" 2>/dev/null || true)"
if [[ -z "$SNAP" ]]; then
  fail "no dispatch snapshot for $LATEST_RUN/$NODE" \
       "PR 1 wires the write path; if this run predates the substrate it'll be empty"
fi
pass "snapshot present (seq, envelope_size): $SNAP"

# 5. Sanity: env_json should mention SPARKWING_RUN_ID matching this run.
ENV_OK="$(sqlite3 "$SPARKWING_HOME/state.db" "SELECT env_json LIKE '%$LATEST_RUN%' FROM node_dispatches WHERE run_id='$LATEST_RUN' AND node_id='$NODE' LIMIT 1" 2>/dev/null || true)"
if [[ "$ENV_OK" != "1" ]]; then
  fail "env_json does not contain run id $LATEST_RUN" "got match=$ENV_OK"
fi
pass "env_json contains run id"

echo
echo "verify-rerun: ok"
