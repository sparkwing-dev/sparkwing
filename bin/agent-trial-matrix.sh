#!/usr/bin/env bash
# Run one agent trial per configuration and print a comparison table.
#
# A conclusion drawn from one agent is worth one agent. This sweeps the
# harnesses and models available locally so a change can be judged
# against more than one set of habits -- they differ in ways that matter
# to CLI design. Claude reads output in 30-60 line windows and issues
# roughly one command per turn; Codex reads 160-240 lines, chains two to
# four commands per turn with `&&`, and filters big output through `rg`
# rather than truncating it. Output that fits the smaller window serves
# both.
#
# Codex authenticated with a ChatGPT account rejects every --model but
# its default, so it varies by reasoning effort instead. That is a
# weaker axis than Claude's and the table labels it as such.
#
# Trials are serialized on purpose: they compete for CPU with anything
# else running, and a contended run has been observed at six times the
# agent's own reported duration.
#
# Usage: agent-trial-matrix.sh [--prompt-file <path>] [--fixture small|miniflux]
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
prompt_file="$REPO_ROOT/internal/agenttrial/testdata/prompts/quickstart.txt"
fixture="small"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prompt-file) prompt_file="${2:-}"; shift ;;
    --fixture)     fixture="${2:-}"; shift ;;
    -h|--help)
      awk 'NR==1 {next} /^#/ {sub(/^# ?/, ""); print; next} {exit}' "$0"
      exit 0
      ;;
    *) echo "agent-trial-matrix: unknown argument $1" >&2; exit 2 ;;
  esac
  shift
done

WORK="${TMPDIR:-/tmp}/sparkwing-agent-trial"

run_one() {
  local label="$1"; shift
  local out
  out=$(bash "$REPO_ROOT/bin/agent-trial.sh" \
    --prompt-file "$prompt_file" --fixture "$fixture" \
    --name "matrix-$label" "$@" 2>&1)
  printf '%s\n' "$out" > "$WORK/matrix-$label.report"

  local log="$WORK/matrix-$label.commands"
  local calls distinct secs wall lint expl
  calls=$(wc -l < "$log" 2>/dev/null | tr -d ' ')
  distinct=$(cut -f2- "$log" 2>/dev/null | sort -u | wc -l | tr -d ' ')
  # time-to-green is the headline: total wall-clock includes the agent
  # writing this harness's FRICTION report, which no user waits for.
  secs=$(printf '%s\n' "$out" | grep -oE 'time-to-green: [0-9]+s' | grep -oE '[0-9]+' | head -1)
  wall=$(printf '%s\n' "$out" | grep -oE 'wall-clock: +[0-9]+s' | grep -oE '[0-9]+' | head -1)
  lint=$(printf '%s\n' "$out" | grep -oE 'lint: +[A-Z]+' | awk '{print $2}')
  expl=$(printf '%s\n' "$out" | grep -oE 'explain: +[A-Z]+' | awk '{print $2}')
  printf '%-18s %5ss %5ss  %3s calls (%2s distinct)  lint=%-5s explain=%s\n' \
    "$label" "${secs:-?}" "${wall:-?}" "${calls:-0}" "${distinct:-0}" "${lint:-?}" "${expl:-?}"
}

echo "prompt:  $prompt_file"
echo "fixture: $fixture"
echo
printf '%-18s %6s %6s  %s\n' config green wall result

if command -v claude >/dev/null 2>&1; then
  run_one claude-opus   --agent claude --model opus
  run_one claude-sonnet --agent claude --model sonnet
  run_one claude-haiku  --agent claude --model haiku
else
  echo "claude not on PATH -- skipped"
fi

if command -v codex >/dev/null 2>&1; then
  run_one codex-low    --agent codex --effort low
  run_one codex-medium --agent codex
  run_one codex-high   --agent codex --effort high
else
  echo "codex not on PATH -- skipped"
fi

echo
echo "reports:  $WORK/matrix-<config>.report"
echo "breakdown: bash $REPO_ROOT/bin/agent-trial-report.sh $WORK/matrix-*.jsonl"
