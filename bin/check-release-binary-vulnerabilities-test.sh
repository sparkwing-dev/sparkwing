#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CASE_ROOT="$(mktemp -d)"
trap 'rm -rf "$CASE_ROOT"' EXIT

mkdir -p "$CASE_ROOT/tools"
touch "$CASE_ROOT/one" "$CASE_ROOT/two"

cat >"$CASE_ROOT/tools/govulncheck" <<'EOF'
#!/usr/bin/env bash
printf '%s\0' "$@" >>"$SCAN_CAPTURE"
if [[ "${SYMBOL_SCAN_STATUS:-0}" -ne 0 && "${2:-}" != "-scan=package" ]]; then
  echo "Vulnerability #1: GO-2026-5932"
  exit "$SYMBOL_SCAN_STATUS"
fi
last="${!#}"
if [[ "$last" == *two ]]; then
  exit "${SECOND_SCAN_STATUS:-0}"
fi
EOF
chmod +x "$CASE_ROOT/tools/govulncheck"

export GOVULNCHECK="$CASE_ROOT/tools/govulncheck"
export SCAN_CAPTURE="$CASE_ROOT/actual"
bash "$ROOT/bin/check-release-binary-vulnerabilities.sh" \
  "$CASE_ROOT/one" "$CASE_ROOT/two"

printf '%s\0' \
  '-mode=binary' "$CASE_ROOT/one" \
  '-mode=binary' "$CASE_ROOT/two" >"$CASE_ROOT/expected"
cmp "$CASE_ROOT/expected" "$CASE_ROOT/actual"

: >"$SCAN_CAPTURE"
export SYMBOL_SCAN_STATUS=3
bash "$ROOT/bin/check-release-binary-vulnerabilities.sh" "$CASE_ROOT/one"
printf '%s\0' \
  '-mode=binary' "$CASE_ROOT/one" >"$CASE_ROOT/expected"
cmp "$CASE_ROOT/expected" "$CASE_ROOT/actual"
unset SYMBOL_SCAN_STATUS

export SECOND_SCAN_STATUS=23
set +e
bash "$ROOT/bin/check-release-binary-vulnerabilities.sh" \
  "$CASE_ROOT/one" "$CASE_ROOT/two"
status=$?
set -e
if [[ $status -ne 23 ]]; then
  echo "release vulnerability scan test: expected exit 23, got $status" >&2
  exit 1
fi

set +e
bash "$ROOT/bin/check-release-binary-vulnerabilities.sh" "$CASE_ROOT/missing"
status=$?
set -e
if [[ $status -ne 2 ]]; then
  echo "release vulnerability scan test: missing artifact exited $status, want 2" >&2
  exit 1
fi

mkdir -p "$CASE_ROOT/fallback-tools"
cat >"$CASE_ROOT/fallback-tools/go" <<'EOF'
#!/usr/bin/env bash
if [[ -n "${GOOS:-}" || -n "${GOARCH:-}" ]]; then
  exit 31
fi
printf '%s\0' "$@" >"$FALLBACK_CAPTURE"
EOF
chmod +x "$CASE_ROOT/fallback-tools/go"

unset GOVULNCHECK
export FALLBACK_CAPTURE="$CASE_ROOT/fallback-actual"
export GOOS=windows
export GOARCH=amd64
PATH="$CASE_ROOT/fallback-tools:/usr/bin:/bin" \
  bash "$ROOT/bin/check-release-binary-vulnerabilities.sh" "$CASE_ROOT/one"
printf '%s\0' \
  run golang.org/x/vuln/cmd/govulncheck@v1.4.0 \
  -mode=binary "$CASE_ROOT/one" >"$CASE_ROOT/fallback-expected"
cmp "$CASE_ROOT/fallback-expected" "$CASE_ROOT/fallback-actual"
