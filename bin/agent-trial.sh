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
# The trial repo is materialized outside this checkout so nothing in the
# agent's working directory points at sparkwing's own pipelines. That is
# a starting position, not a sandbox: the agent has a shell and can read
# anything this user can, and trials have been observed listing the
# sparkwing checkout. Read the orientation path before trusting a run.
#
# Two fixtures, measuring different things:
#
#   --fixture small     (default) the committed
#                       internal/agenttrial/testdata/trial-repo. Fast,
#                       offline, no existing CI. Measures authoring a
#                       pipeline from a description.
#
#   --fixture miniflux  a pinned commit of miniflux/v2 (Apache-2.0, 87
#                       packages, 133 SQL migrations), cloned and cached
#                       on first use. It ships ten GitHub Actions
#                       workflows, so an agent will read and translate
#                       them: this measures migrating an existing CI
#                       setup, which is a different job than authoring
#                       from scratch. Do not compare the two numbers.
#
# Usage: agent-trial.sh --prompt-file <path> [--name <trial>]
#                       [--fixture small|miniflux]
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
fixture="small"

# A real upstream project, pinned. Cloned at trial time rather than
# vendored: this repo does not need to redistribute someone else's
# Apache-2.0 tree, and the pin is what keeps two runs comparable.
MINIFLUX_REPO="https://github.com/miniflux/v2.git"
MINIFLUX_SHA="69756868dd1edbe62801c3a2a214c59d286320ce"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prompt-file) prompt_file="${2:-}"; shift ;;
    --prompt)      prompt_text="${2:-}"; shift ;;
    --name)        name="${2:-}"; shift ;;
    --fixture)     fixture="${2:-}"; shift ;;
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

# The fixture is fixed input, not something generated here, so two runs
# a week apart are comparable and a change in the number is a change in
# sparkwing rather than in the scaffolding.
case "$fixture" in
  small)
    FIXTURE="$REPO_ROOT/internal/agenttrial/testdata/trial-repo"
    [[ -d "$FIXTURE" ]] || { echo "agent-trial: missing fixture $FIXTURE" >&2; exit 1; }
    cp -R "$FIXTURE/." "$TRIAL/"
    ;;
  miniflux)
    # Cache the pinned tree so repeat trials do not refetch it.
    CACHE="$WORK/cache-miniflux-$MINIFLUX_SHA"
    if [[ ! -d "$CACHE" ]]; then
      echo "fetching pinned fixture $MINIFLUX_SHA ..."
      rm -rf "$CACHE.tmp"
      mkdir -p "$CACHE.tmp"
      (
        cd "$CACHE.tmp" || exit 1
        git init -q .
        git remote add origin "$MINIFLUX_REPO"
        git fetch -q --depth 1 origin "$MINIFLUX_SHA"
        git checkout -q FETCH_HEAD
      ) || { echo "agent-trial: failed to fetch $MINIFLUX_REPO@$MINIFLUX_SHA" >&2; exit 1; }
      rm -rf "$CACHE.tmp/.git"
      mv "$CACHE.tmp" "$CACHE"
    fi
    cp -R "$CACHE/." "$TRIAL/"
    ;;
  *)
    echo "agent-trial: unknown fixture $fixture (want small or miniflux)" >&2
    exit 2
    ;;
esac

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
#
# Compare resolved paths: on macOS the trial lives under $TMPDIR in
# /var/..., which is a symlink to /private/var/..., and an agent that
# resolves it reports the second form. Comparing the literal strings
# calls every run an escape.
trial_real="$(cd "$TRIAL" && pwd -P)"
escaped=$(cmds | grep -oE 'cd +/[^ &;|]*' | awk '{print $2}' \
  | sed 's|^/private/|/|' \
  | awk -v a="$TRIAL" -v b="${trial_real#/private}" '$0 !~ "^"a && $0 !~ "^"b {print}' \
  | sort -u)
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
