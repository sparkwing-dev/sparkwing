#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <run-handle> <sparkwing-binary>" >&2
  exit 2
fi

handle="$1"
sparkwing_binary="$2"
if [ ! -s "$handle" ]; then
  echo "canonical run handle was not published; the failure happened before Sparkwing accepted the run" >&2
  exit 0
fi

run_id="$(jq -er '.run_id | select(type == "string" and length > 0)' "$handle")"
result=0
"$sparkwing_binary" runs status --run "$run_id" -o json || result=$?
"$sparkwing_binary" runs logs --run "$run_id" --tail 500 -o plain || result=$?
exit "$result"
