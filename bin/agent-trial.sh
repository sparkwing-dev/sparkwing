#!/usr/bin/env bash
# Time an agent going from an empty repo to working pipelines, and show
# the path it took to get there.
#
# This answers a different question than bin/acceptance-pipelines.sh.
# That one scores generated source against the oracle bar; it says
# nothing about how an agent arrives at the source, because it hands the
# model the docs and takes one shot. This runs the real product flow: a
# fresh repo with no .sparkwing/, a real agent, the real CLI, and the
# template catalog -- which is how a pipeline is actually supposed to get
# written. Authoring from a blank file is the fallback, not the path.
#
# The report is the point. Wall-clock says whether the flow is fast
# enough; the ORIENTATION PATH says why it wasn't. A step the agent
# repeats, a doc it re-reads, or SDK source it opens out of the module
# cache is a question the tooling should have answered the first time.
#
# The trial repo is a committed fixture (internal/agenttrial/testdata/
# trial-repo) copied outside this checkout, so the agent cannot read
# this repo's own pipelines or the acceptance corpus, and two runs
# weeks apart stay comparable.
#
# Usage: agent-trial.sh --prompt-file <path> [--name <trial>]
#        agent-trial.sh --prompt "build me a lint pipeline"
#
# Requires: claude + jq on PATH. Costs real model calls.
set -uo pipefail

# Anchor on this script's own location rather than the caller's working
# directory: the trial cd's into a throwaway repo, and resolving the
# fixture through `git rev-parse` from wherever the caller happened to
# stand silently yields the wrong tree (or an empty one).
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
prompt_file=""
prompt_text=""
name="trial"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prompt-file) prompt_file="${2:-}"; shift ;;
    --prompt)      prompt_text="${2:-}"; shift ;;
    --name)        name="${2:-}"; shift ;;
    -h|--help)
      awk 'NR==1 {next} /^#/ {sub(/^# ?/, ""); print; next} {exit}' "$0"
      exit 0
      ;;
    *) echo "agent-trial: unknown argument $1" >&2; exit 2 ;;
  esac
  shift
done

for tool in claude jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "agent-trial: $tool not found on PATH" >&2; exit 1; }
done

WORK="${TMPDIR:-/tmp}/sparkwing-agent-trial"
TRIAL="$WORK/$name"
TRACE="$WORK/$name.jsonl"
mkdir -p "$WORK"
rm -rf "${WORK:?}/${name:?}"
mkdir -p "$TRIAL"

if [[ -n "$prompt_text" ]]; then
  prompt_file="$WORK/$name.prompt"
  printf '%s\n' "$prompt_text" > "$prompt_file"
fi
[[ -n "$prompt_file" && -r "$prompt_file" ]] || { echo "agent-trial: need --prompt or --prompt-file" >&2; exit 2; }

# The fixture is a committed repository, not something generated here,
# so two runs a week apart are comparable and a change in the number is
# a change in sparkwing rather than in the scaffolding.
FIXTURE="$REPO_ROOT/internal/agenttrial/testdata/trial-repo"
[[ -d "$FIXTURE" ]] || { echo "agent-trial: missing fixture $FIXTURE" >&2; exit 1; }
cp -R "$FIXTURE/." "$TRIAL/"

cd "$TRIAL" || exit 1
git init -q .
git add -A && git -c user.email=trial@example.com -c user.name=trial commit -qm init

echo "trial repo: $TRIAL (no .sparkwing/)"
echo "prompt:     $(head -c 120 "$prompt_file")..."
echo

# bypassPermissions: the agent must run `sparkwing` and write files
# without a prompt, and this is a throwaway repo. It is still an
# unrestricted agent on this machine -- that is the cost of measuring
# the real flow rather than a sandboxed imitation of it.
#
# A machine-local agent bootstrap (e.g. a personal ~/.claude/CLAUDE.md
# pointing at internal tooling) will hijack the trial: an agent that
# treats the prompt as tracked work moves into some other workspace and
# the run measures that instead. Asking it not to via
# --append-system-prompt does not hold, so the report detects the
# escape below rather than pretending to prevent it.
START=$(date +%s)
claude --print --output-format stream-json --verbose \
  --permission-mode bypassPermissions \
  < "$prompt_file" > "$TRACE" 2>"$WORK/$name.err"
agent_exit=$?
ELAPSED=$(( $(date +%s) - START ))

echo "agent finished in ${ELAPSED}s (exit $agent_exit)"
echo

cmds() {
  jq -r 'select(.type=="assistant") | .message.content[]?
         | select(.type=="tool_use" and .name=="Bash") | .input.command' "$TRACE" 2>/dev/null
}

echo "=== ORIENTATION PATH (sparkwing invocations, in order) ==="
cmds | grep -oE '\bsparkwing [a-z][a-z0-9 _-]*' | sed 's/ *$//' | awk '!seen[$0]++ || $0 != prev {print "  " NR ". " $0} {prev=$0}' | head -30
echo

echo "=== WASTE SIGNALS ==="
total_bash=$(cmds | wc -l | tr -d ' ')
docs_reads=$(cmds | grep -cE 'sparkwing docs read')
docs_uniq=$(cmds | grep -oE 'docs read --topic [a-z/-]+' | sort -u | wc -l | tr -d ' ')
sdk_source=$(cmds | grep -cE 'pkg/mod.*sparkwing|go doc ')
printf '  %-38s %s\n' "total shell commands:" "$total_bash"
printf '  %-38s %s (%s distinct topics)\n' "docs read calls:" "$docs_reads" "$docs_uniq"
printf '  %-38s %s\n' "reads of SDK source / go doc:" "$sdk_source"

# An agent that cd's somewhere else is no longer being measured. Report
# it as a failed trial, not as a slow one.
escaped=$(cmds | grep -oE 'cd +/[^ &;|]*' | awk -v t="$TRIAL" '$2 !~ "^"t {print $2}' | sort -u)
if [[ -n "$escaped" ]]; then
  echo "  -> LEFT THE TRIAL REPO. Work done here is not being measured:"
  printf '       %s\n' $escaped | head -5
fi
if [[ "$docs_reads" -gt "$docs_uniq" ]]; then
  echo "  -> re-read $(( docs_reads - docs_uniq )) doc topic(s): one pass did not answer the question"
fi
if [[ "$sdk_source" -gt 0 ]]; then
  echo "  -> fell back to reading source: an API it needed is not in the docs"
fi
echo

echo "=== RESULT ==="
if [[ ! -d "$TRIAL/.sparkwing" ]]; then
  echo "  NO .sparkwing/ IN THE TRIAL REPO -- the agent built elsewhere."
  echo "  Treat the timing above as void; see the escape note in WASTE SIGNALS."
fi
sparkwing pipeline list -o json 2>/dev/null | jq -r '.[] | "  \(.name)\t\(.short)"' 2>/dev/null || echo "  (no pipelines registered)"
echo
echo "  lint:    $(sparkwing pipeline lint --all >/dev/null 2>&1 && echo PASS || echo FAIL)"
echo "  explain: $(sparkwing pipeline explain --all -o json >/dev/null 2>&1 && echo PASS || echo FAIL)"
echo
echo "wall-clock: ${ELAPSED}s   trace: $TRACE   repo: $TRIAL"
