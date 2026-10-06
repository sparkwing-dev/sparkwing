#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CASE_ROOT="$(mktemp -d)"
trap 'rm -rf "$CASE_ROOT"' EXIT

REPO="$CASE_ROOT/repo"
mkdir -p "$REPO/bin"
cp "$ROOT/bin/install.sh" "$REPO/bin/install.sh"
cp "$ROOT/bin/web-build-lock.sh" "$REPO/bin/web-build-lock.sh"
git -C "$REPO" init -q
git -C "$REPO" -c user.email=t@t -c user.name=t add bin/install.sh
git -C "$REPO" -c user.email=t@t -c user.name=t \
  -c commit.gpgsign=false -c core.hooksPath=/dev/null commit -qm fixture

STUB="$CASE_ROOT/stub"
mkdir -p "$STUB"
printf '#!/usr/bin/env bash\nexec %q "$@"\n' "$(command -v git)" >"$STUB/git"
chmod +x "$STUB/git"
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "$*" >>"${MKDIR_LOG:-/dev/null}"\nif [[ "${@: -1}" == "${EXISTING_DEST_MUST_NOT_CREATE:-}" ]]; then echo "existing destination traversed" >&2; exit 1; fi\nexec %q "$@"\n' "$(command -v mkdir)" >"$STUB/mkdir"
chmod +x "$STUB/mkdir"
cat >"$STUB/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${ARGV_LOG:-/dev/null}"
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
cat >"$STUB/uname" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "${FAKE_UNAME:-Linux}"
EOF
cat >"$STUB/cygpath" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
path="${@: -1}"
path="${path//\\//}"
case "$path" in
  C:/fixture/*) printf '%s/%s\n' "$FAKE_WINDOWS_ROOT" "${path#C:/fixture/}" ;;
  *) printf '%s\n' "$path" ;;
esac
EOF
chmod +x "$STUB/uname" "$STUB/cygpath"

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

dest1="$CASE_ROOT/dest1"
if ! run_install SPARKWING_INSTALL_BIN="$dest1" FAKE_GOPATH="$CASE_ROOT/gopath" \
  >"$CASE_ROOT/out1" 2>&1; then
  fail "HOME-unset install failed; it must succeed once DEST is known" "$CASE_ROOT/out1"
fi
[ -x "$dest1/sparkwing" ] || fail "HOME-unset install left no binary at $dest1/sparkwing" "$CASE_ROOT/out1"
if grep -q "unbound variable" "$CASE_ROOT/out1"; then
  fail "HOME-unset install tripped set -u" "$CASE_ROOT/out1"
fi

if run_install FAKE_GOPATH="$CASE_ROOT/gopath" >"$CASE_ROOT/out2" 2>&1; then
  fail "install with no HOME and no DEST succeeded; it cannot know where to write" "$CASE_ROOT/out2"
fi
grep -q "set SPARKWING_INSTALL_BIN" "$CASE_ROOT/out2" \
  || fail "no-DEST refusal does not name the remedy" "$CASE_ROOT/out2"

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

dest4="$CASE_ROOT/dest4"
rival4="$CASE_ROOT/rival collision/sparkwing"
mkdir -p "$(dirname "$rival4")"
printf '#!/bin/sh\n# source\nexit 0\n' >"$rival4"
rival4_content="$(cat "$rival4")"
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
[ "$(cat "$rival4")" = "$rival4_content" ] \
  || fail "collision remedy changed the source" "$CASE_ROOT/out4"
[ "$(cat "$rival4.superseded")" = "existing destination" ] \
  || fail "collision remedy overwrote the existing destination" "$CASE_ROOT/out4"

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
[ "$(cat "$rival4")" = "$rival4_content" ] \
  || fail "racing remedy changed the source" "$CASE_ROOT/out4"
[ "$(cat "$rival4.superseded")" = "raced destination" ] \
  || fail "racing remedy overwrote the destination" "$CASE_ROOT/out4"

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

argvlog="$CASE_ROOT/argv.log"
dest6="$CASE_ROOT/dest6"
mkdir -p "$dest6"
printf 'old runner\n' >"$dest6/sparkwing-runner"
if ! run_install SPARKWING_INSTALL_BIN="$dest6" FAKE_GOPATH="$CASE_ROOT/gopath" \
  ARGV_LOG="$argvlog" >"$CASE_ROOT/out6" 2>&1; then
  fail "install for the build-flag check failed" "$CASE_ROOT/out6"
fi
buildargv="$(grep -F -- ' build ' "$argvlog" | head -n1 || true)"
[ -n "$buildargv" ] || fail "install ran no go build" "$argvlog"
for want in "-trimpath" "-s -w -X main.Version="; do
  case "$buildargv" in
    *"$want"*) ;;
    *) fail "install build argv is missing $want: $buildargv" ;;
  esac
done
[ -x "$dest6/sparkwing-runner" ] \
  || fail "install removed the runner instead of updating it" "$CASE_ROOT/out6"
grep -qF './cmd/sparkwing-runner' "$argvlog" \
  || fail "install did not build the runner from this tree" "$argvlog"

mkdir -p "$REPO/web/out"
printf 'prebuilt dashboard\n' >"$REPO/web/out/index.html"
dest7="$CASE_ROOT/dest7"
if ! env -u HOME -u GOBIN -u SKIP_WEB_BUILD \
  PATH="$STUB:/usr/bin:/bin" SPARKWING_INSTALL_BIN="$dest7" \
  FAKE_GOPATH="$CASE_ROOT/gopath" bash "$REPO/bin/install.sh" \
  >"$CASE_ROOT/out7" 2>&1; then
  fail "install without pnpm failed despite a prebuilt web/out" "$CASE_ROOT/out7"
fi
cmp "$REPO/web/out/index.html" "$REPO/internal/web/next-out/index.html" \
  || fail "install did not embed the prebuilt dashboard" "$CASE_ROOT/out7"
grep -q 'pnpm.*unavailable' "$CASE_ROOT/out7" \
  || fail "install did not explain the web build fallback" "$CASE_ROOT/out7"

rm -rf "$REPO/web/out" "$REPO/internal/web/next-out"
dest8="$CASE_ROOT/dest8"
if ! env -u HOME -u GOBIN -u SKIP_WEB_BUILD \
  PATH="$STUB:/usr/bin:/bin" SPARKWING_INSTALL_BIN="$dest8" \
  FAKE_GOPATH="$CASE_ROOT/gopath" bash "$REPO/bin/install.sh" \
  >"$CASE_ROOT/out8" 2>&1; then
  fail "install without pnpm or web assets failed" "$CASE_ROOT/out8"
fi
grep -q 'dashboard.*unavailable' "$CASE_ROOT/out8" \
  || fail "install did not warn that its dashboard is unavailable" "$CASE_ROOT/out8"

windows_root="$CASE_ROOT/windows"
mkdir -p "$windows_root/dest" "$windows_root/go path/bin" "$windows_root/other go/bin" "$windows_root/rival dir"
touch "$windows_root/dest/sparkwing-cache.exe" "$windows_root/dest/sparkwing-cache"
touch "$windows_root/go path/bin/sparkwing-local-ws.exe"
printf '#!/bin/sh\nexit 0\n' >"$windows_root/rival dir/sparkwing.exe"
chmod +x "$windows_root/rival dir/sparkwing.exe"
windows_log="$CASE_ROOT/windows-argv.log"
if ! run_install SPARKWING_INSTALL_BIN='C:\fixture\dest' \
  GOBIN='C:\fixture\rival dir' FAKE_UNAME=MINGW64_NT-10.0 \
  FAKE_WINDOWS_ROOT="$windows_root" FAKE_GOPATH='C:\fixture\go path;C:\fixture\other go' \
  ARGV_LOG="$windows_log" >"$CASE_ROOT/out-windows" 2>&1; then
  fail "Git Bash install failed with native Windows paths" "$CASE_ROOT/out-windows"
fi
[ -x "$windows_root/dest/sparkwing.exe" ] && [ -x "$windows_root/dest/sparkwing-runner.exe" ] \
  || fail "Windows install did not build both .exe outputs" "$CASE_ROOT/out-windows"
[ -z "$(find "$windows_root/dest" -maxdepth 1 -name sparkwing -print)" ] \
  || fail "Windows install created an extensionless output" "$CASE_ROOT/out-windows"
[ ! -e "$windows_root/dest/sparkwing-cache.exe" ] && [ ! -e "$windows_root/dest/sparkwing-cache" ] \
  || fail "Windows install retained retired outputs in DEST" "$CASE_ROOT/out-windows"
grep -qF "stale $windows_root/go path/bin/sparkwing-local-ws.exe" "$CASE_ROOT/out-windows" \
  || fail "Windows GOPATH lost its drive letter or semicolon boundary" "$CASE_ROOT/out-windows"
[ -e "$windows_root/go path/bin/sparkwing-local-ws.exe" ] \
  || fail "Windows stale report modified a file outside DEST" "$CASE_ROOT/out-windows"
grep -qF "another sparkwing is installed at $windows_root/rival dir/sparkwing.exe" "$CASE_ROOT/out-windows" \
  || fail "Windows rival from native GOBIN went unreported" "$CASE_ROOT/out-windows"
named_dest="$windows_root/named"
mkdir -p "$named_dest"
printf 'existing main\n' >"$named_dest/sparkwing.exe"
printf 'existing runner\n' >"$named_dest/sparkwing-runner.exe"
if ! run_install SPARKWING_INSTALL_BIN='C:\fixture\named' SPARKWING_INSTALL_NAME=sparkwing-windows \
  FAKE_UNAME=MSYS_NT-10.0 FAKE_WINDOWS_ROOT="$windows_root" \
  >"$CASE_ROOT/out-windows-named" 2>&1; then
  fail "named Windows install failed" "$CASE_ROOT/out-windows-named"
fi
[ -x "$named_dest/sparkwing-windows.exe" ] \
  || fail "named Windows build did not receive .exe suffix" "$CASE_ROOT/out-windows-named"
[ "$(cat "$named_dest/sparkwing.exe")" = 'existing main' ] \
  && [ "$(cat "$named_dest/sparkwing-runner.exe")" = 'existing runner' ] \
  || fail "named Windows install replaced an operational binary" "$CASE_ROOT/out-windows-named"
if ! run_install SPARKWING_INSTALL_BIN="$dest1" EXISTING_DEST_MUST_NOT_CREATE="$dest1" \
  FAKE_UNAME=MINGW64_NT-10.0 \
  >"$CASE_ROOT/out-existing" 2>&1; then
  fail "idempotent install unnecessarily recreated its existing destination" "$CASE_ROOT/out-existing"
fi
for unix_uname in Linux Darwin; do
  unix_mkdir_log="$CASE_ROOT/$unix_uname-mkdir.log"
  if ! run_install SPARKWING_INSTALL_BIN="$dest1" MKDIR_LOG="$unix_mkdir_log" \
    FAKE_UNAME="$unix_uname" >"$CASE_ROOT/out-existing-$unix_uname" 2>&1; then
    fail "$unix_uname install failed with an existing destination" "$CASE_ROOT/out-existing-$unix_uname"
  fi
  grep -qFx -- "-p $dest1" "$unix_mkdir_log" \
    || fail "$unix_uname install stopped calling mkdir -p for an existing destination" "$CASE_ROOT/out-existing-$unix_uname"
done
echo "install-test: ok"
