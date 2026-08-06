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
# The trial repo is created outside this checkout, so the agent cannot
# read this repo's own pipelines or the acceptance corpus.
#
# Usage: agent-trial.sh --prompt-file <path> [--name <trial>]
#        agent-trial.sh --prompt "build me a lint pipeline"
#
# Requires: claude + jq on PATH. Costs real model calls.
set -uo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
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

# A small but plausible service: something for requests about linting,
# testing, image builds, and migrations to actually bite on.
cd "$TRIAL" || exit 1
git init -q .
printf 'module example.com/app\n\ngo 1.26.0\n' > go.mod
mkdir -p cmd/app migrations
printf 'package main\n\nimport "fmt"\n\nfunc main() { fmt.Println("hello") }\n' > cmd/app/main.go
printf 'package main\n\nimport "testing"\n\nfunc TestNothing(t *testing.T) {}\n' > cmd/app/main_test.go
cat > Dockerfile <<'DOCKERFILE'
FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN go build -o /out/app ./cmd/app
FROM gcr.io/distroless/base
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
DOCKERFILE
git add -A && git -c user.email=trial@example.com -c user.name=trial commit -qm init

echo "trial repo: $TRIAL (no .sparkwing/)"
echo "prompt:     $(head -c 120 "$prompt_file")..."
echo

# bypassPermissions: the agent must run `sparkwing` and write files
# without a prompt, and this is a throwaway repo. It is still an
# unrestricted agent on this machine -- that is the cost of measuring
# the real flow rather than a sandboxed imitation of it.
#
# The appended line keeps a machine-local agent bootstrap (a personal
# ~/.claude/CLAUDE.md pointing at unrelated internal services) from
# showing up as product latency. A customer repo has no such services.
START=$(date +%s)
claude --print --output-format stream-json --verbose \
  --permission-mode bypassPermissions \
  --append-system-prompt 'This repository is standalone. No company-internal services, ticketing systems, or knowledge bases exist on this machine; do not attempt to consult any.' \
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
if [[ "$docs_reads" -gt "$docs_uniq" ]]; then
  echo "  -> re-read $(( docs_reads - docs_uniq )) doc topic(s): one pass did not answer the question"
fi
if [[ "$sdk_source" -gt 0 ]]; then
  echo "  -> fell back to reading source: an API it needed is not in the docs"
fi
echo

echo "=== RESULT ==="
sparkwing pipeline list -o json 2>/dev/null | jq -r '.[] | "  \(.name)\t\(.short)"' 2>/dev/null || echo "  (no pipelines registered)"
echo
echo "  lint:    $(sparkwing pipeline lint --all >/dev/null 2>&1 && echo PASS || echo FAIL)"
echo "  explain: $(sparkwing pipeline explain --all -o json >/dev/null 2>&1 && echo PASS || echo FAIL)"
echo
echo "wall-clock: ${ELAPSED}s   trace: $TRACE   repo: $TRIAL"
