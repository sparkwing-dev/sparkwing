#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

generator=fixture
output=pretty
spec=""
repeat=""
save_dir=""
revise=""

while [[ $# -gt 0 ]]; do
  # shellcheck disable=SC2209
  case "$1" in
    --live) generator=command ;;
    --json) output=json ;;
    --spec) spec="${2:-}"; shift ;;
    --repeat) repeat="${2:-}"; shift ;;
    --save-dir) save_dir="${2:-}"; shift ;;
    --revise) revise="${2:-}"; shift ;;
    -h|--help)
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
