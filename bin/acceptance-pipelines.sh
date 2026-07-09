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
# Usage: acceptance-pipelines.sh [--live] [--json] [--spec <name>]
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"

generator=fixture
output=pretty
spec=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --live) generator=command ;;
    --json) output=json ;;
    --spec) spec="${2:-}"; shift ;;
    -h|--help)
      sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'
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

go -C "$REPO_ROOT" run ./internal/pipelineaccept "${args[@]}"
