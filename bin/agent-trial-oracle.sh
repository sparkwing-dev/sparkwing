#!/usr/bin/env bash
# Reads the repository an agent trial left behind and answers the two
# questions the trial report asks of it: which pipelines the agent
# registered, and whether the run did what the prompt asked.
#
# Usage:
#   agent-trial-oracle.sh list <trial-dir>
#   agent-trial-oracle.sh task <trial-dir> <expect-file>
#
# Expectations live beside each prompt as <prompt>.expect, one per line;
# blank lines and # comments are ignored:
#   trigger <event>   some pipeline declares this trigger
#   yaml <regex>      sparkwing.yaml matches
#   job <regex>       some file under .sparkwing/jobs/ matches
#   pipelines <n>     at least n pipelines are registered
set -uo pipefail

command -v jq >/dev/null 2>&1 || {
  echo "agent-trial-oracle: jq not found on PATH" >&2
  exit 1
}

records() {
  (cd "$1" 2>/dev/null && sparkwing pipeline list -o json 2>/dev/null)
}

# `pipeline list -o json` emits NDJSON, one record per line. Slurping and
# flattening reads that and a single JSON array alike, so neither shape
# silently reports zero pipelines.
cmd_list() {
  records "$1" | jq -s -r 'flatten(1)[] | "\(.name)\t\(.short // "")"' 2>/dev/null
}

cmd_count() {
  records "$1" | jq -s -r 'flatten(1) | length' 2>/dev/null || echo 0
}

cmd_task() {
  local trial="$1" expect="$2"
  if [[ ! -r "$expect" ]]; then
    echo "  task:    (no $(basename "$expect"); lint+explain do not check whether the prompt was satisfied)"
    return 0
  fi
  local yaml_all jobs_all pipeline_count task_fail task_ok kind arg ok
  yaml_all=$(cat "$trial"/.sparkwing/sparkwing.yaml 2>/dev/null)
  jobs_all=$(cat "$trial"/.sparkwing/jobs/*.go 2>/dev/null)
  pipeline_count=$(cmd_count "$trial")
  task_fail=""
  task_ok=0
  while read -r kind arg; do
    [[ -z "$kind" || "$kind" == \#* ]] && continue
    ok=1
    case "$kind" in
      trigger)   grep -qE "^[[:space:]]*$arg:" <<<"$yaml_all" || ok=0 ;;
      yaml)      grep -qE "$arg" <<<"$yaml_all" || ok=0 ;;
      job)       grep -qE "$arg" <<<"$jobs_all" || ok=0 ;;
      pipelines) [[ "${pipeline_count:-0}" -ge "$arg" ]] || ok=0 ;;
      *)         echo "  (unknown expectation kind $kind)" ; continue ;;
    esac
    if [[ "$ok" -eq 1 ]]; then
      task_ok=$((task_ok + 1))
    else
      task_fail="$task_fail
    missing: $kind $arg"
    fi
  done < "$expect"
  if [[ -n "$task_fail" ]]; then
    echo "  task:    FAIL -- compiles and lints, but does not do what was asked$task_fail"
  else
    echo "  task:    PASS ($task_ok expectation(s))"
  fi
}

case "${1:-}" in
  list) cmd_list "${2:?trial directory required}" ;;
  task) cmd_task "${2:?trial directory required}" "${3:?expect file required}" ;;
  *)    echo "agent-trial-oracle: usage: $(basename "$0") list|task <trial-dir> [expect-file]" >&2; exit 2 ;;
esac
