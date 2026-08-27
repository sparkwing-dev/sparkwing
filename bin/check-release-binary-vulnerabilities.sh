#!/usr/bin/env bash
# Scan the exact Go binaries selected for publication. Source scans can miss
# build-tag and platform differences, so the release workflow calls this after
# each matrix cell builds its artifact and before upload.
set -euo pipefail

if [[ $# -eq 0 ]]; then
  echo "usage: check-release-binary-vulnerabilities.sh <binary>..." >&2
  exit 2
fi

if [[ -n "${GOVULNCHECK:-}" ]]; then
  if ! command -v "$GOVULNCHECK" >/dev/null 2>&1; then
    echo "release vulnerability scan: govulncheck not found: $GOVULNCHECK" >&2
    exit 2
  fi
  scanner=("$GOVULNCHECK")
elif command -v govulncheck >/dev/null 2>&1; then
  scanner=(govulncheck)
elif command -v go >/dev/null 2>&1; then
  # Matrix targets belong to the artifact, not to this host-side scanner.
  # A leaked GOOS/GOARCH would build a scanner this runner cannot execute.
  scanner=(env -u GOOS -u GOARCH -u GOAMD64 -u GOARM64 go run golang.org/x/vuln/cmd/govulncheck@v1.4.0)
else
  echo "release vulnerability scan: neither govulncheck nor go is available" >&2
  exit 2
fi

for artifact in "$@"; do
  if [[ ! -f "$artifact" ]]; then
    echo "release vulnerability scan: artifact not found: $artifact" >&2
    exit 2
  fi
  echo "==> govulncheck -mode=binary $artifact"
  "${scanner[@]}" -mode=binary "$artifact"
done
