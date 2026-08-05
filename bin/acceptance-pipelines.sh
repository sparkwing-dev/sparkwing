#!/usr/bin/env bash
# Run the AI-pipeline acceptance harness: generate every corpus spec and
# score it through the gofmt/compile/vet/explain/lint oracle bar. Exit
# nonzero if any spec disagrees with its expectation -- a regression in the
# pipeline templates, the authoring guide, the linter, or the SDK pin.
#
# The default (fixture) generator is deterministic and needs no model: it is
# the regression gate, suitable for CI or a scheduled chore. Pass --live to
# drive a cold authoring model instead (bin/pipeline-cold-author.sh; needs
# the claude CLI + jq), turning the run into a real "can a cold agent author
# a working pipeline" check.
#
# A live cold author is stochastic: the same spec can compile on one
# attempt and not the next. Pass --repeat N to sample each spec N times
# so the pass rate is a measurement rather than a coin flip, and
# --save-dir DIR to keep every generation's source for diagnosis.
#
# Pass --revise N to give a failing generation N rounds of oracle
# feedback before scoring it, matching the documented
# "first or second try" acceptance bar (--revise 1).
#
# Usage: acceptance-pipelines.sh [--live] [--json] [--spec <name>]
#                                [--repeat <n>] [--save-dir <dir>]
#                                [--revise <n>]
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"

generator=fixture
output=pretty
spec=""
repeat=""
save_dir=""
revise=""

while [[ $# -gt 0 ]]; do
  # --live's generator=command is a literal mode value, not a substitution.
  # shellcheck disable=SC2209
  case "$1" in
    --live) generator=command ;;
    --json) output=json ;;
    --spec) spec="${2:-}"; shift ;;
    --repeat) repeat="${2:-}"; shift ;;
    --save-dir) save_dir="${2:-}"; shift ;;
    --revise) revise="${2:-}"; shift ;;
    -h|--help)
      # Print the header comment block, stopping at the first
      # non-comment line, so adding usage lines never silently
      # truncates --help the way a hardcoded line range does.
      awk 'NR==1 {next} /^#/ {sub(/^# ?/, ""); print; next} {exit}' "$0"
      exit 0
      ;;
    *) echo "acceptance-pipelines: unknown argument $1" >&2; exit 2 ;;
  esac
  shift
done

args=(--generator "$generator" --output "$output")
if [[ "$generator" == command ]]; then
  args+=(--command "bash $REPO_ROOT/bin/pipeline-cold-author.sh")
fi
if [[ -n "$spec" ]]; then
  args+=(--spec "$spec")
fi
if [[ -n "$repeat" ]]; then
  args+=(--repeat "$repeat")
fi
if [[ -n "$save_dir" ]]; then
  args+=(--save-dir "$save_dir")
fi
if [[ -n "$revise" ]]; then
  args+=(--revise "$revise")
fi

go -C "$REPO_ROOT" run ./internal/pipelineaccept "${args[@]}"
