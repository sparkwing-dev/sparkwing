#!/usr/bin/env bash
set -euo pipefail
umask 077

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <run-handle> <sparkwing-binary> <output-file>" >&2
  exit 2
fi

handle="$1"
sparkwing_binary="$2"
output="$3"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

if [ ! -s "$handle" ]; then
  jq -n '{format_version: 1, availability: "unavailable", reason: "no_run_handle"}' >"$output"
  exit 0
fi
run_id="$(jq -er '.run_id | select(type == "string" and length > 0)' "$handle")"
if ! "$sparkwing_binary" runs status --run "$run_id" --exit-zero -o json >"$scratch/status.json"; then
  jq -n --arg run_id "$run_id" \
    '{format_version: 1, availability: "unavailable", reason: "status_query_failed", run_id: $run_id}' >"$output"
  exit 1
fi

# safety: status also contains invocation arguments, errors and local paths.
# Publish only named timing fields because artifacts outlive the private runner.
jq -e --arg run_id "$run_id" \
  --arg source_sha "${SOURCE_SHA:-}" \
  --arg workflow_run_id "${GITHUB_RUN_ID:-}" \
  --arg workflow_attempt "${GITHUB_RUN_ATTEMPT:-}" \
  --arg runner_os "${RUNNER_OS:-}" \
  --arg runner_arch "${RUNNER_ARCH:-}" '
  if .run.id != $run_id or (.nodes | type) != "array" then
    error("run status does not match the requested run")
  else
    {
      format_version: 1,
      availability: "available",
      provenance: {
        source_sha: $source_sha,
        workflow_run_id: $workflow_run_id,
        workflow_attempt: $workflow_attempt,
        runner_os: $runner_os,
        runner_arch: $runner_arch
      },
      run: (.run | {id, pipeline, status, repo, git_sha, created_at, started_at, finished_at}),
      nodes: [.nodes[] | {
        id, status, outcome, started_at, finished_at, execution_started_at,
        cpu_nanos, max_rss_bytes, process_wall_nanos,
        steps: [(.steps // [])[] | {step_id, status, started_at, finished_at}]
      }],
      limitations: [
        "pipeline compilation and hosted setup precede these run timings",
        "node start delay is not an admission-only measurement",
        "cache temperature and reserved runner capacity are not established",
        "null metrics mean unavailable rather than zero",
        "a partial run retains its observed status and unfinished timestamps"
      ]
    }
  end' "$scratch/status.json" >"$scratch/timings.json"
mv "$scratch/timings.json" "$output"
