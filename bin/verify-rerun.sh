#!/usr/bin/env bash

set -euo pipefail

GREEN="\033[32m"
RED="\033[31m"
DIM="\033[2m"
RESET="\033[0m"

pass() { echo -e "  ${GREEN}PASS${RESET} $1"; }
fail() { echo -e "  ${RED}FAIL${RESET} $1 ${DIM}(${2:-})${RESET}"; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
export SPARKWING_HOME="$WORK"
mkdir -p "$WORK"

echo "==> building binaries"
go build -o "$WORK/sparkwing" ./cmd/sparkwing
export PATH="$WORK:$PATH"


echo "==> looking for a recent local run"
LATEST_RUN="$(sparkwing runs list --limit 1 -o json | python3 -c 'import json,sys; rs=json.load(sys.stdin).get("runs", []); print(rs[0]["id"]) if rs else None' 2>/dev/null || true)"
if [[ -z "$LATEST_RUN" || "$LATEST_RUN" == "None" ]]; then
  echo "no runs in local store -- run a pipeline first (e.g. sparkwing run test) and re-run this script"
  exit 0
fi
pass "found latest run: $LATEST_RUN"

NODE="$(sparkwing runs status --run "$LATEST_RUN" -o json | python3 -c 'import json,sys; ns=json.load(sys.stdin).get("nodes", []); print(ns[0]["node_id"]) if ns else None' 2>/dev/null || true)"
if [[ -z "$NODE" || "$NODE" == "None" ]]; then
  fail "could not pick a node from run $LATEST_RUN"
fi
pass "node: $NODE"

SNAP="$(sqlite3 "$SPARKWING_HOME/state.db" "SELECT seq, length(input_envelope_json) FROM node_dispatches WHERE run_id='$LATEST_RUN' AND node_id='$NODE' ORDER BY seq DESC LIMIT 1" 2>/dev/null || true)"
if [[ -z "$SNAP" ]]; then
  fail "no dispatch snapshot for $LATEST_RUN/$NODE" \
       "PR 1 wires the write path; if this run predates the substrate it'll be empty"
fi
pass "snapshot present (seq, envelope_size): $SNAP"

ENV_OK="$(sqlite3 "$SPARKWING_HOME/state.db" "SELECT env_json LIKE '%$LATEST_RUN%' FROM node_dispatches WHERE run_id='$LATEST_RUN' AND node_id='$NODE' LIMIT 1" 2>/dev/null || true)"
if [[ "$ENV_OK" != "1" ]]; then
  fail "env_json does not contain run id $LATEST_RUN" "got match=$ENV_OK"
fi
pass "env_json contains run id"

echo
echo "verify-rerun: ok"
