#!/usr/bin/env bash
# Cold-author driver for the acceptance harness. Read a natural-language
# pipeline spec on stdin, ask a fresh model (claude --print) to author the
# .sparkwing/jobs/candidate.go source given ONLY the authoring guide and the
# SDK reference, and write that Go source to stdout. Empty output is a
# failure.
#
# Each invocation is an independent session with no shared state -- the "cold
# agent" the harness scores. Used by acceptance-pipelines.sh --live as the
# --command generator. Requires the claude CLI and jq on PATH.
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"

for tool in claude jq; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "pipeline-cold-author: $tool not found on PATH" >&2
    exit 1
  fi
done

guide="$(cat "$REPO_ROOT/docs/authoring-pipelines.md")"
sdkref="$(cat "$REPO_ROOT/docs/sdk-reference.md")"
spec="$(cat)"

system="You author sparkwing CI/CD pipelines in Go. A pipeline is a Go file in
package jobs that registers itself with sw.Register and implements Plan. Output
ONLY the raw Go source for .sparkwing/jobs/candidate.go: no markdown fences, no
prose, no explanation. Obey every rule in the authoring guide below.

=== AUTHORING GUIDE ===
${guide}

=== SDK REFERENCE ===
${sdkref}"

result="$(claude --print --output-format json --system-prompt "$system" <<<"$spec")"
source="$(jq -r '.result' <<<"$result")"

# Drop any markdown code fences the model added around the source.
source="$(printf '%s\n' "$source" | sed -e '/^```/d')"

if [[ -z "$(printf '%s' "$source" | tr -d '[:space:]')" ]]; then
  echo "pipeline-cold-author: model produced no source" >&2
  exit 1
fi

printf '%s\n' "$source"
