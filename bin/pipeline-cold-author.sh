#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

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

source="$(printf '%s\n' "$source" | sed -e '/^```/d')"

if [[ -z "$(printf '%s' "$source" | tr -d '[:space:]')" ]]; then
  echo "pipeline-cold-author: model produced no source" >&2
  exit 1
fi

printf '%s\n' "$source"
