#!/usr/bin/env bash
set -euo pipefail
umask 077

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <run-handle> <sparkwing-binary> <log-file>" >&2
  exit 2
fi

handle="$1"
sparkwing_binary="$2"
log_file="$3"
if [ ! -s "$handle" ]; then
  echo "no run handle is available; cannot locate stored diagnostics" >&2
  exit 0
fi

run_id="$(jq -er '.run_id | select(type == "string" and length > 0)' "$handle")"
result=0
"$sparkwing_binary" runs status --run "$run_id" -o json || result=$?
"$sparkwing_binary" runs logs --run "$run_id" -o plain >"$log_file" 2>&1 || result=$?
tail -n 500 "$log_file"
exit "$result"
