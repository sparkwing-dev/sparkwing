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
# Three fixtures, measuring different things. Do not compare their
# numbers to each other:
#
#   --fixture small     (default) the committed
#                       internal/agenttrial/testdata/trial-repo. Fast,
#                       offline, no existing CI. Measures authoring a
#                       pipeline from a description.
#
#   --fixture migrate   the same tree plus a GitHub Actions workflow,
#                       applied as an overlay so the two differ in
#                       exactly one thing. Measures translating CI that
#                       already exists -- the agent has a spec to read
#                       instead of a description to interpret, which is
#                       the more common way anyone actually arrives.
#
#   --fixture miniflux  a pinned commit of miniflux/v2 (Apache-2.0, 87
#                       packages, 133 SQL migrations), cloned and cached
#                       on first use. It ships ten GitHub Actions
#                       workflows, so an agent will read and translate
#                       them: this measures migrating an existing CI
#                       setup at real-world scale, against CI nobody
#                       wrote for this test.
#
# --agent selects the harness under test (claude, codex) and --model the
# model within it. Command counts come from a PATH shim that logs every
# `sparkwing` invocation, not from any agent's transcript format, so the
# numbers mean the same thing across agents. Design conclusions drawn
# from one agent's habits are worth exactly one agent.
#
# Usage: agent-trial.sh --prompt-file <path> [--name <trial>]
#                       [--fixture small|migrate|miniflux]
#                       [--agent claude|codex] [--model <model>] [--effort low|medium|high]
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
agent="claude"
model=""
effort=""

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
    --agent)       agent="${2:-}"; shift ;;
    --model)       model="${2:-}"; shift ;;
    --effort)      effort="${2:-}"; shift ;;
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
# Absolute before the cd below: a relative --prompt-file is relative to
# the caller, and resolving it from inside the trial repo silently feeds
# the agent nothing.
prompt_file="$(cd "$(dirname "$prompt_file")" && pwd)/$(basename "$prompt_file")"

# The fixture is fixed input, not something generated here, so two runs
# a week apart are comparable and a change in the number is a change in
# sparkwing rather than in the scaffolding.
case "$fixture" in
  small)
    FIXTURE="$REPO_ROOT/internal/agenttrial/testdata/trial-repo"
    [[ -d "$FIXTURE" ]] || { echo "agent-trial: missing fixture $FIXTURE" >&2; exit 1; }
    cp -R "$FIXTURE/." "$TRIAL/"
    ;;
  migrate)
    # The same repo plus a GitHub Actions workflow, as an overlay rather
    # than a second copy of the tree: migrating existing CI and authoring
    # from nothing have to differ in exactly one thing, or the two
    # numbers are not comparable.
    FIXTURE="$REPO_ROOT/internal/agenttrial/testdata/trial-repo"
    OVERLAY="$REPO_ROOT/internal/agenttrial/testdata/gha-overlay"
    [[ -d "$FIXTURE" ]] || { echo "agent-trial: missing fixture $FIXTURE" >&2; exit 1; }
    [[ -d "$OVERLAY" ]] || { echo "agent-trial: missing overlay $OVERLAY" >&2; exit 1; }
    cp -R "$FIXTURE/." "$TRIAL/"
    cp -R "$OVERLAY/." "$TRIAL/"
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
    echo "agent-trial: unknown fixture $fixture (want small, migrate, or miniflux)" >&2
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
# Every sparkwing invocation is logged by a shim ahead of the real
# binary on PATH. Transcript formats differ per agent and change under
# us; argv does not, so the command trace is the one measurement that
# means the same thing for all of them.
SHIM="$WORK/$name.shim"
CMDLOG="$WORK/$name.commands"
rm -rf "$SHIM"; mkdir -p "$SHIM"
: > "$CMDLOG"
real_sparkwing="$(command -v sparkwing)"
# Logs when each invocation started AND finished. The finish time of
# the last one is time-to-green: everything after it is the agent
# composing its closing message, which is this harness's own overhead
# and not something a user waits for. Reporting only total wall-clock
# billed the product ~12s per run for writing a friction report.
cat > "$SHIM/sparkwing" <<SHIMEOF
#!/usr/bin/env bash
start=\$(date +%s)
"$real_sparkwing" "\$@"
rc=\$?
printf '%s\t%s\t%s\t%s\n' "\$start" "\$(date +%s)" "\$rc" "\$*" >> "$CMDLOG"
exit \$rc
SHIMEOF
chmod +x "$SHIM/sparkwing"
export PATH="$SHIM:$PATH"

START=$(date +%s)
case "$agent" in
  claude)
    claude --print --output-format stream-json --verbose \
      --permission-mode bypassPermissions \
      ${model:+--model "$model"} \
      < "$prompt_file" > "$TRACE" 2>"$WORK/$name.err"
    agent_exit=$?
    ;;
  codex)
    # </dev/null because codex exec appends stdin to the prompt and
    # waits for EOF to do it. With stdin inherited from a pipe that
    # nobody closes -- a backgrounded sweep, say -- it prints "Reading
    # additional input from stdin..." and blocks forever, which reads
    # as a slow agent rather than a stuck harness.
    #
    # Reasoning effort goes through -c rather than a flag: a ChatGPT
    # account rejects every --model but its default, so effort is the
    # only axis that varies here.
    codex exec --json --dangerously-bypass-approvals-and-sandbox \
      ${model:+--model "$model"} \
      ${effort:+-c "model_reasoning_effort=\"$effort\""} \
      "$(cat "$prompt_file")" < /dev/null > "$TRACE" 2>"$WORK/$name.err"
    agent_exit=$?
    ;;
  *)
    echo "agent-trial: unknown agent $agent (want claude or codex)" >&2
    exit 2
    ;;
esac
ELAPSED=$(( $(date +%s) - START ))

# Stop logging before this script starts running sparkwing itself. The
# scoring below is not the agent's work, and counting it made every
# agent look like it ran four commands it never ran -- including one
# that ran none.
PATH="${PATH#"$SHIM":}"
AGENT_CALLS=$(wc -l < "$CMDLOG" | tr -d ' ')

# Time-to-green: the last sparkwing invocation's finish. Everything
# after it is the agent writing its closing FRICTION report, which this
# harness asked for and no user waits for -- ~12s a run, or a fifth of
# the total, billed to the product for the privilege of being measured.
GREEN=""
if [[ -s "$CMDLOG" ]]; then
  last_end=$(awk -F'\t' '$2 != "" {e=$2} END {print e}' "$CMDLOG")
  [[ -n "$last_end" ]] && GREEN=$(( last_end - START ))
fi
if [[ -n "$GREEN" ]]; then
  echo "agent finished in ${ELAPSED}s (exit $agent_exit); ${GREEN}s to the last sparkwing call"
else
  echo "agent finished in ${ELAPSED}s (exit $agent_exit)"
fi
echo

# A report for a run that never happened reads exactly like a report for
# a run that failed on the merits. Stop here instead.
if [[ "$agent_exit" -ne 0 ]] || [[ ! -s "$TRACE" ]]; then
  echo "agent-trial: the agent did not run to completion; no measurement to report." >&2
  echo "  stderr: $WORK/$name.err" >&2
  tail -5 "$WORK/$name.err" >&2
  exit 1
fi

cmds() {
  jq -r 'select(.type=="assistant") | .message.content[]?
         | select(.type=="tool_use" and .name=="Bash") | .input.command' "$TRACE" 2>/dev/null
}

# An agent whose shell rebuilds PATH from a login profile never sees the
# shim. Fall back to its transcript, which is per-agent but is the only
# record left; say which source the numbers came from either way.
if [[ "$AGENT_CALLS" -eq 0 ]] && [[ -s "$TRACE" ]]; then
  # Codex runs each command through `zsh -lc`, and a login shell
  # rebuilds PATH from the profile, so the shim never sees it. Its
  # transcript records executions as typed events; read those rather
  # than grepping prose, which counted every mention of a command in
  # the agent's own reasoning as a call and turned eleven into 176.
  jq -r 'select(.type=="item.completed") | select(.item.type=="command_execution")
         | .item.command' "$TRACE" 2>/dev/null \
    | grep -oE '\bsparkwing [a-z][a-z0-9 ._=<>/-]*' \
    | sed 's/^sparkwing //' | awk 'NF {print "\t\t\t" $0}' > "$CMDLOG"
  AGENT_CALLS=$(wc -l < "$CMDLOG" | tr -d ' ')
  CMD_SOURCE="transcript (shim bypassed by login shell)"
else
  CMD_SOURCE="PATH shim"
fi

echo "=== ORIENTATION PATH (sparkwing invocations, in order) ==="
cut -f4- "$CMDLOG" | awk '{print "  " NR ". sparkwing " $0}' | cut -c1-100 | head -30
echo

echo "=== WASTE SIGNALS ==="
printf '  %-38s %s (%s)\n' "sparkwing invocations:" "$AGENT_CALLS" "$CMD_SOURCE"
printf '  %-38s %s\n' "  distinct:" "$(cut -f4- "$CMDLOG" | sort -u | wc -l | tr -d ' ')"
printf '  %-38s %s\n' "  repeated verbatim:" "$(cut -f4- "$CMDLOG" | sort | uniq -d | wc -l | tr -d ' ')"
docs_reads=$(cut -f4- "$CMDLOG" | grep -cE '^docs read')
docs_uniq=$(cut -f4- "$CMDLOG" | grep -oE 'docs read --topic [a-z/-]+' | sort -u | wc -l | tr -d ' ')
printf '  %-38s %s (%s distinct topics)\n' "docs read calls:" "$docs_reads" "$docs_uniq"

# Everything below reads the agent's own transcript, whose shape is
# per-agent. Absent one, the shim numbers above still stand.
if [[ "$agent" == "claude" ]]; then
  total_bash=$(jq -r 'select(.type=="assistant") | .message.content[]?
                      | select(.type=="tool_use") | .name' "$TRACE" 2>/dev/null \
    | wc -l | tr -d ' ')
  sdk_source=$(cmds | grep -cE 'pkg/mod.*sparkwing|go doc ')
  printf '  %-38s %s\n' "total tool calls:" "$total_bash"
  printf '  %-38s %s\n' "reads of SDK source / go doc:" "$sdk_source"
fi

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
if [[ "${sdk_source:-0}" -gt 0 ]]; then
  echo "  -> fell back to reading source: an API it needed is not in the docs"
fi
echo

# The agent is the only witness to what it had to guess at. A prompt
# that asks for a FRICTION section turns that into a report line rather
# than something to reconstruct from the trace afterwards.
#
# Each harness buries the closing message somewhere different: claude in
# a final `result` field, codex in the last `agent_message` item. Both
# store it as one JSON-escaped line, so the raw-text fallback below only
# catches an agent that prints it unencoded -- it cannot substitute for
# knowing the shape.
extract_friction() { awk '/^[^a-z]*FRICTION:/{found=1} found{print}'; }
friction=$(jq -r 'select(.type=="result") | .result' "$TRACE" 2>/dev/null | extract_friction)
if [[ -z "$friction" ]]; then
  friction=$(jq -r 'select(.item.type=="agent_message") | .item.text' "$TRACE" 2>/dev/null | extract_friction)
fi
if [[ -z "$friction" ]]; then
  friction=$(extract_friction < "$TRACE" 2>/dev/null)
fi
if [[ -n "$friction" ]]; then
  echo "=== FRICTION (agent-reported) ==="
  printf '%s\n' "$friction" | head -40
  echo
fi

echo "=== RESULT ==="
if [[ ! -d "$TRIAL/.sparkwing" ]]; then
  echo "  NO .sparkwing/ IN THE TRIAL REPO -- the agent built elsewhere."
  echo "  Treat the timing above as void; see the escape note in WASTE SIGNALS."
fi
sparkwing pipeline list -o json 2>/dev/null | jq -r '.[] | "  \(.name)\t\(.short)"' 2>/dev/null || echo "  (no pipelines registered)"
echo
echo "  lint:    $(sparkwing pipeline lint --all >/dev/null 2>&1 && echo PASS || echo FAIL)"

# Explain each pipeline separately under --sw-dry-run. `explain --all`
# refuses a pipeline whose steps carry risk labels, and rejects the
# --sw-dry-run that would waive them ("--all does not accept
# pipeline-specific flags"), so scoring with it marks a correct
# prod-deploy pipeline as broken.
explain_ok=0
explain_bad=""
for p in $(sparkwing pipeline list -o json 2>/dev/null | jq -r '.[].name' 2>/dev/null); do
  if sparkwing pipeline explain --name "$p" --sw-dry-run -o json >/dev/null 2>&1; then
    explain_ok=$((explain_ok + 1))
  else
    explain_bad="$explain_bad $p"
  fi
done
if [[ -n "$explain_bad" ]]; then
  echo "  explain: FAIL ($explain_ok ok; failed:$explain_bad)"
else
  echo "  explain: PASS ($explain_ok pipelines)"
fi

# lint and explain say a pipeline is well-formed. Neither knows what the
# prompt asked for, and the gap is not theoretical: a trial scored clean
# here having produced a pipeline with no trigger at all -- it compiled,
# linted, explained, ran green, reported no friction, and was the
# fastest run in its sweep. Every signal said it was the best result.
#
# The expectations live beside each prompt as <prompt>.expect and are
# deliberately coarse. They answer "did it do the task", not "did it do
# the task the way I would have".
EXPECT="${prompt_file%.txt}.expect"
if [[ -r "$EXPECT" ]]; then
  yaml_all=$(cat "$TRIAL"/.sparkwing/sparkwing.yaml 2>/dev/null)
  jobs_all=$(cat "$TRIAL"/.sparkwing/jobs/*.go 2>/dev/null)
  pipeline_count=$(sparkwing pipeline list -o json 2>/dev/null | jq -r 'length' 2>/dev/null || echo 0)
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
  done < "$EXPECT"
  if [[ -n "$task_fail" ]]; then
    echo "  task:    FAIL -- compiles and lints, but does not do what was asked$task_fail"
  else
    echo "  task:    PASS ($task_ok expectation(s))"
  fi
else
  echo "  task:    (no $(basename "$EXPECT"); lint+explain do not check whether the prompt was satisfied)"
fi
echo
# The agent records its own elapsed time. A harness wall-clock much
# larger than that is this machine being busy, not sparkwing being slow
# -- a trial run alongside a build has been observed at 6x the agent's
# own number. Print both so a contended run is obvious instead of
# quietly becoming a data point.
agent_ms=$(tail -1 "$TRACE" 2>/dev/null | jq -r '.duration_ms // empty' 2>/dev/null)
if [[ -n "$GREEN" ]]; then
  echo "time-to-green: ${GREEN}s   (+$(( ELAPSED - GREEN ))s writing the FRICTION report: harness overhead, not product)"
fi
if [[ -n "$agent_ms" ]]; then
  agent_s=$((agent_ms / 1000))
  echo "wall-clock:    ${ELAPSED}s (agent reports ${agent_s}s)"
  if [[ "$agent_s" -gt 0 && $((ELAPSED / agent_s)) -ge 2 ]]; then
    echo "  -> harness clock is >=2x the agent's own: this machine was busy; rerun idle"
  fi
else
  echo "wall-clock:    ${ELAPSED}s"
fi
echo "trace: $TRACE"
echo "repo:  $TRIAL"
