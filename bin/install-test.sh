#!/usr/bin/env bash
# Regression tests for bin/install.sh's post-install reporting, run
# against a stub `go` so no real build happens.
#
# What must hold:
#   1. With HOME unset (a stripped CI or launchd context) and a DEST
#      given, the install exits 0 and leaves its binary -- set -u must
#      never kill the competing-install report that runs after the
#      install already succeeded.
#   2. With neither HOME nor SPARKWING_INSTALL_BIN, the script refuses
#      up front with an actionable message, not an unbound-variable trace.
#   3. A printed retire remedy survives the paste that applies it: the
#      mv for a rival under a directory with spaces is executed verbatim
#      and must move exactly that rival.
#   4. A multi-element GOPATH resolves to the FIRST element's bin, never
#      to "<a>:<b>/bin".
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CASE_ROOT="$(mktemp -d)"
trap 'rm -rf "$CASE_ROOT"' EXIT

REPO="$CASE_ROOT/repo"
mkdir -p "$REPO/bin"
cp "$ROOT/bin/install.sh" "$REPO/bin/install.sh"
git -C "$REPO" init -q
git -C "$REPO" -c user.email=t@t -c user.name=t add bin/install.sh
git -C "$REPO" -c user.email=t@t -c user.name=t \
  -c commit.gpgsign=false -c core.hooksPath=/dev/null commit -qm fixture

# Stub `go`: answers `go env GOPATH` from $FAKE_GOPATH and materializes
# `go ... build ... -o <dest>` without compiling.
STUB="$CASE_ROOT/stub"
mkdir -p "$STUB"
cat >"$STUB/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case " $* " in
  *" env GOPATH "*) printf '%s\n' "${FAKE_GOPATH:-}"; exit 0 ;;
esac
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
if [ -n "$out" ]; then
  printf '#!/bin/sh\nexit 0\n' >"$out"
  chmod +x "$out"
fi
EOF
chmod +x "$STUB/go"

run_install() {
  env -u HOME -u GOBIN -u SPARKWING_INSTALL_BIN SKIP_WEB_BUILD=1 \
    PATH="$STUB:/usr/bin:/bin" "$@" bash "$REPO/bin/install.sh"
}

fail() {
  echo "install-test: $1" >&2
  shift
  for f in "$@"; do cat "$f" >&2; done
  exit 1
}

# Case 1: HOME unset, DEST given -- exit 0 and the binary is installed.
dest1="$CASE_ROOT/dest1"
if ! run_install SPARKWING_INSTALL_BIN="$dest1" FAKE_GOPATH="$CASE_ROOT/gopath" \
  >"$CASE_ROOT/out1" 2>&1; then
  fail "HOME-unset install failed; it must succeed once DEST is known" "$CASE_ROOT/out1"
fi
[ -x "$dest1/sparkwing" ] || fail "HOME-unset install left no binary at $dest1/sparkwing" "$CASE_ROOT/out1"
if grep -q "unbound variable" "$CASE_ROOT/out1"; then
  fail "HOME-unset install tripped set -u" "$CASE_ROOT/out1"
fi

# Case 2: HOME and SPARKWING_INSTALL_BIN both unset -- refuse up front,
# with the remedy named.
if run_install FAKE_GOPATH="$CASE_ROOT/gopath" >"$CASE_ROOT/out2" 2>&1; then
  fail "install with no HOME and no DEST succeeded; it cannot know where to write" "$CASE_ROOT/out2"
fi
grep -q "set SPARKWING_INSTALL_BIN" "$CASE_ROOT/out2" \
  || fail "no-DEST refusal does not name the remedy" "$CASE_ROOT/out2"

# Case 3: a rival install under a directory with spaces -- the printed
# mv must be paste-safe, proven by pasting it.
dest3="$CASE_ROOT/dest three"
rivaldir="$CASE_ROOT/rival dir"
mkdir -p "$rivaldir"
printf '#!/bin/sh\nexit 0\n' >"$rivaldir/sparkwing"
chmod +x "$rivaldir/sparkwing"
if ! env -u HOME -u GOBIN SKIP_WEB_BUILD=1 \
  PATH="$STUB:$rivaldir:/usr/bin:/bin" \
  SPARKWING_INSTALL_BIN="$dest3" FAKE_GOPATH="$CASE_ROOT/gopath" \
  bash "$REPO/bin/install.sh" >"$CASE_ROOT/out3" 2>&1; then
  fail "install beside a rival failed; the report must never fail the install" "$CASE_ROOT/out3"
fi
grep -qF "another sparkwing is installed at $rivaldir/sparkwing" "$CASE_ROOT/out3" \
  || fail "rival under a spaced directory went unreported" "$CASE_ROOT/out3"
remedy="$(sed -n 's/.*to retire it: \(.*\)   (undo.*/\1/p' "$CASE_ROOT/out3" | head -n1)"
[ -n "$remedy" ] || fail "no retire remedy printed for the rival" "$CASE_ROOT/out3"
eval "$remedy"
[ -e "$rivaldir/sparkwing.superseded" ] && [ ! -e "$rivaldir/sparkwing" ] \
  || fail "pasting the remedy did not retire exactly the rival: $remedy" "$CASE_ROOT/out3"

# Case 4: an existing retirement destination must never be overwritten.
dest4="$CASE_ROOT/dest4"
rival4="$CASE_ROOT/rival collision/sparkwing"
mkdir -p "$(dirname "$rival4")"
printf 'source\n' >"$rival4"
chmod +x "$rival4"
printf 'existing destination\n' >"$rival4.superseded"
if ! env -u HOME -u GOBIN SKIP_WEB_BUILD=1 \
  PATH="$STUB:$(dirname "$rival4"):/usr/bin:/bin" \
  SPARKWING_INSTALL_BIN="$dest4" FAKE_GOPATH="$CASE_ROOT/gopath" \
  bash "$REPO/bin/install.sh" >"$CASE_ROOT/out4" 2>&1; then
  fail "install beside a retirement collision failed; reporting must stay read-only" "$CASE_ROOT/out4"
fi
remedy4="$(sed -n 's/.*to retire it: \(.*\)   (undo.*/\1/p' "$CASE_ROOT/out4" | head -n1)"
[ -n "$remedy4" ] || fail "no guarded remedy printed for the collision" "$CASE_ROOT/out4"
if eval "$remedy4"; then
  fail "collision remedy reported success instead of refusing the existing destination" "$CASE_ROOT/out4"
fi
[ "$(cat "$rival4")" = "source" ] \
  || fail "collision remedy changed the source" "$CASE_ROOT/out4"
[ "$(cat "$rival4.superseded")" = "existing destination" ] \
  || fail "collision remedy overwrote the existing destination" "$CASE_ROOT/out4"

# Even if the destination appears after the guards, mv -n must preserve it
# and the source postcondition must make the command fail visibly instead of
# reporting a successful retirement that never happened.
race_stub="$CASE_ROOT/race-stub"
mkdir -p "$race_stub"
cat >"$race_stub/mv" <<'EOF'
#!/bin/sh
destination=""
for argument in "$@"; do destination="$argument"; done
printf 'raced destination\n' >"$destination"
exec /bin/mv "$@"
EOF
chmod +x "$race_stub/mv"
rm "$rival4.superseded"
if (PATH="$race_stub:/usr/bin:/bin"; eval "$remedy4"); then
  fail "remedy hid a destination race instead of failing its postcondition" "$CASE_ROOT/out4"
fi
[ "$(cat "$rival4")" = "source" ] \
  || fail "racing remedy changed the source" "$CASE_ROOT/out4"
[ "$(cat "$rival4.superseded")" = "raced destination" ] \
  || fail "racing remedy overwrote the destination" "$CASE_ROOT/out4"

# Case 5: multi-element GOPATH -- stale binaries are looked for in the
# first element's bin, and "<a>:<b>/bin" is never constructed.
gp1="$CASE_ROOT/gp1"
gp2="$CASE_ROOT/gp2"
mkdir -p "$gp1/bin" "$gp2/bin"
touch "$gp1/bin/sparkwing-local-ws"
dest5="$CASE_ROOT/dest5"
if ! run_install SPARKWING_INSTALL_BIN="$dest5" FAKE_GOPATH="$gp1:$gp2" \
  >"$CASE_ROOT/out5" 2>&1; then
  fail "install with a multi-element GOPATH failed" "$CASE_ROOT/out5"
fi
grep -qF "stale $gp1/bin/sparkwing-local-ws" "$CASE_ROOT/out5" \
  || fail "stale binary in the first GOPATH element's bin went unreported" "$CASE_ROOT/out5"
grep -qF "$gp1:$gp2/bin" "$CASE_ROOT/out5" \
  && fail "the whole GOPATH list was glued to /bin" "$CASE_ROOT/out5"
[ -e "$gp1/bin/sparkwing-local-ws" ] \
  || fail "the stale-binary report modified a file outside DEST" "$CASE_ROOT/out5"

echo "install-test: ok"
