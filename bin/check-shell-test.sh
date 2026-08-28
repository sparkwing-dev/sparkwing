#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CASE_ROOT="$(mktemp -d)"
trap 'rm -rf "$CASE_ROOT"' EXIT

mkdir -p "$CASE_ROOT/repo/bin" "$CASE_ROOT/repo/scripts" "$CASE_ROOT/tools"
cp "$ROOT/bin/check-shell.sh" "$CASE_ROOT/repo/bin/check-shell.sh"

printf '#!/usr/bin/env bash\n' >"$CASE_ROOT/repo/bin/alpha.sh"
printf '#!/usr/bin/env bash\n' >"$CASE_ROOT/repo/bin/[.sh"
printf '#!/usr/bin/env bash\n' >"$CASE_ROOT/repo/bin/B.sh"
printf '#!/bin/sh\n' >"$CASE_ROOT/repo/bin/command with spaces"
newline_script=$'bin/newline\nalpha.sh'
printf '#!/usr/bin/env bash\n' >"$CASE_ROOT/repo/$newline_script"
printf '#!/usr/bin/env bash\n' >"$CASE_ROOT/repo/bin/é.sh"
printf '#!/usr/bin/env bash\n' >"$CASE_ROOT/repo/scripts/beta script.sh"
printf '#!/usr/bin/env bash\n' >"$CASE_ROOT/repo/scripts/z command"
printf 'plain text\n' >"$CASE_ROOT/repo/bin/not-a-script"

git -C "$CASE_ROOT/repo" init -q
git -C "$CASE_ROOT/repo" add \
  'bin/[.sh' \
  "bin/alpha.sh" \
  'bin/B.sh' \
  "bin/command with spaces" \
  "$newline_script" \
  "bin/not-a-script" \
  'bin/é.sh' \
  "scripts/beta script.sh" \
  "scripts/z command"

cat >"$CASE_ROOT/tools/shellcheck" <<'EOF'
#!/usr/bin/env bash
printf '%s\0' "$@" >"$CHECK_SHELL_CAPTURE"
exit "${CHECK_SHELL_STATUS:-0}"
EOF
chmod +x "$CASE_ROOT/tools/shellcheck"

export CHECK_SHELL_CAPTURE="$CASE_ROOT/actual"
export PATH="$CASE_ROOT/tools:$PATH"
export LC_ALL=en_US.UTF-8
bash "$CASE_ROOT/repo/bin/check-shell.sh"

printf '%s\0' \
  '--severity=warning' \
  '--shell=bash' \
  'bin/B.sh' \
  'bin/[.sh' \
  'bin/alpha.sh' \
  'bin/command with spaces' \
  "$newline_script" \
  'bin/é.sh' \
  'scripts/beta script.sh' \
  'scripts/z command' \
  >"$CASE_ROOT/expected"
cmp "$CASE_ROOT/expected" "$CASE_ROOT/actual"

export CHECK_SHELL_STATUS=23
set +e
bash "$CASE_ROOT/repo/bin/check-shell.sh"
status=$?
set -e

if [[ $status -ne 23 ]]; then
  echo "check-shell-test: expected shellcheck exit 23, got $status" >&2
  exit 1
fi

bash "$ROOT/bin/chaos-soak-runner-test.sh"
