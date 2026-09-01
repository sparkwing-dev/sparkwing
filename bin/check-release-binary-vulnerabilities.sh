#!/usr/bin/env bash
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
  if output="$("${scanner[@]}" -mode=binary "$artifact" 2>&1)"; then
    printf '%s\n' "$output"
    continue
  else
    scan_status=$?
  fi
  printf '%s\n' "$output"
  findings="$(printf '%s\n' "$output" | sed -n 's/^Vulnerability #[0-9][0-9]*: \(GO-[0-9-][0-9-]*\)$/\1/p' | sort -u)"
  if [[ "$findings" == "GO-2026-5932" ]] && \
      ! go list -deps ./... 2>/dev/null | grep -Eq '^golang.org/x/crypto/openpgp(/|$)'; then
    echo "release vulnerability scan: GO-2026-5932 does not reach an OpenPGP package"
    continue
  fi
  exit "$scan_status"
done
