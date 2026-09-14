#!/usr/bin/env bash
# Times the demo path end to end: install the CLI the way an adopter does,
# scaffold a pipeline, compile it, and run it to green. Prints the seconds from
# install to first green run, with each phase broken out.
#
# Every phase starts from a HOME the harness creates, so the Go module cache,
# the Go build cache and the sparkwing home are all empty: the number covers the
# dependency download an adopter actually waits for.
#
# Exit status: 0 measured, 1 the demo path did not reach green, 2 bad usage,
# 75 the modules could not be downloaded so nothing was measured.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

readonly EXIT_UNAVAILABLE=75

usage() {
  cat <<'EOF'
usage: bash bin/install-to-green.sh [flags]

  --build              Build cmd/sparkwing from this checkout, install it as a
                       locally staged signed release, and point the scaffolded
                       pipeline at this worktree's SDK (default).
  --binary PATH        Stage PATH as the release asset instead of building one.
                       The scaffold pins the SDK version PATH reports.
  --release            Install from the published release over the network,
                       which is the documented install path verbatim.
  --version TAG        Release tag for --release (default: latest).
  --template SHAPE     Pipeline template to scaffold (default: minimal).
  --target-seconds N   Exit non-zero when the total exceeds N seconds.
  --output FORMAT      pretty (default) or json.
  --inherit-go-env     Keep the caller's GOPROXY, GOFLAGS, GOTOOLCHAIN and the
                       other module-resolution variables instead of resetting
                       them to the public defaults.
  --keep               Leave the scratch tree behind and print its path.
  -h, --help           This message.
EOF
}

fail() {
  printf 'install-to-green: %s\n' "$*" >&2
  exit 1
}

# A skipped measurement is still a record: the caller logs the phase that could
# not resolve and when, rather than a bare status.
unavailable() {
  printf '{"measured":false,"green":false,"phase":"%s","started_at":"%s","reason":"%s"}\n' \
    "$1" "$started_at" "$(json_string "$2")"
  printf 'install-to-green: %s phase: %s, so nothing was measured\n' "$1" "$2" >&2
  exit "$EXIT_UNAVAILABLE"
}

mode=build
binary=""
version=""
template=minimal
target_seconds=""
output=pretty
keep=0
inherit_go_env=0

while (($#)); do
  case "$1" in
    --build) mode=build; shift ;;
    --binary) (($# >= 2)) || fail "--binary needs a path"; mode=binary; binary="$2"; shift 2 ;;
    --binary=*) mode=binary; binary="${1#--binary=}"; shift ;;
    --release) mode=release; shift ;;
    --version) (($# >= 2)) || fail "--version needs a release tag"; version="$2"; shift 2 ;;
    --version=*) version="${1#--version=}"; shift ;;
    --template) (($# >= 2)) || fail "--template needs a shape"; template="$2"; shift 2 ;;
    --template=*) template="${1#--template=}"; shift ;;
    --target-seconds) (($# >= 2)) || fail "--target-seconds needs a number"; target_seconds="$2"; shift 2 ;;
    --target-seconds=*) target_seconds="${1#--target-seconds=}"; shift ;;
    --output) (($# >= 2)) || fail "--output needs a format"; output="$2"; shift 2 ;;
    --output=*) output="${1#--output=}"; shift ;;
    --inherit-go-env) inherit_go_env=1; shift ;;
    --keep) keep=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'install-to-green: unknown flag: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$output" in
  pretty|json) ;;
  *) fail "--output takes pretty or json, got $output" ;;
esac
if [[ -n "$target_seconds" && ! "$target_seconds" =~ ^[0-9]+$ ]]; then
  fail "--target-seconds takes a whole number of seconds, got $target_seconds"
fi
if [[ "$mode" == binary && ! -x "$binary" ]]; then
  fail "--binary $binary is not an executable file"
fi
if [[ -n "$version" && "$mode" != release ]]; then
  fail "--version names a published release and only applies to --release"
fi

# A clock with a nanosecond field is not universal: BSD date passes %N through
# as text, so the probe reads the output rather than the platform name.
clock="date"
if [[ "$(date +%s%N)" =~ [^0-9] ]]; then
  command -v python3 >/dev/null 2>&1 ||
    fail "this date has no nanosecond field and python3 is not on PATH, so no phase can be timed"
  clock=python
fi

now_ns() {
  if [[ "$clock" == date ]]; then
    date +%s%N
  else
    python3 -c 'import time; print(time.time_ns())'
  fi
}

# Phases are truncated to milliseconds once and the total is summed from those,
# so the printed columns add up to the printed total.
milliseconds_of() { printf '%d' "$(($1 / 1000000))"; }
seconds_of() { printf '%d.%03d' "$(($1 / 1000))" "$(($1 % 1000))"; }

load_average() {
  if [[ -r /proc/loadavg ]]; then
    cut -d' ' -f1 /proc/loadavg
  elif command -v sysctl >/dev/null 2>&1 && sysctl -n vm.loadavg >/dev/null 2>&1; then
    sysctl -n vm.loadavg | awk '{print $2}'
  else
    printf 'unknown'
  fi
}

json_string() {
  printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/\t/\\t/g'
}

json_number() {
  if [[ "$1" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
    printf '%s' "$1"
  else
    printf 'null'
  fi
}

core_count() {
  if command -v nproc >/dev/null 2>&1; then
    nproc
  elif command -v sysctl >/dev/null 2>&1 && sysctl -n hw.ncpu >/dev/null 2>&1; then
    sysctl -n hw.ncpu
  else
    getconf _NPROCESSORS_ONLN 2>/dev/null || printf '0'
  fi
}

# A Go failure that is the network rather than the change under measurement.
# Reporting one as a slow demo path would publish a number nothing produced.
download_unavailable() {
  tail -n 25 "$1" |
    grep -Eq 'dial tcp|no such host|i/o timeout|TLS handshake timeout|connection refused|network is unreachable|module lookup disabled|502 Bad Gateway|503 Service Unavailable|504 Gateway Time-?out'
}

# Git binds a working context through the environment, and a hook, `git rebase
# --exec` or `git bisect run` passes that binding down. Left set, the demo
# repository's init and commit land in the caller's repository instead. This is
# the pkg/gitenv binding set.
for bound in GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_PREFIX GIT_COMMON_DIR \
  GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_NAMESPACE GIT_QUARANTINE_PATH; do
  unset "$bound"
done

cores="$(core_count)"
load_start="$(load_average)"
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# The Go toolchain has to be resolved before HOME moves, because a version
# manager's shim reads the old HOME to find the real binary.
command -v go >/dev/null 2>&1 || fail "go is required to compile the scaffolded pipeline and is not on PATH"
GO_BIN_DIR="$(go env GOROOT)/bin"
[[ -x "$GO_BIN_DIR/go" ]] || fail "go env GOROOT does not contain a go binary at $GO_BIN_DIR/go"

WORK="$(mktemp -d 2>/dev/null || mktemp -d -t sparkwing-install-to-green)"
DEMO_HOME="$WORK/home"
PREFIX="$DEMO_HOME/.local/bin"
REPO="$WORK/demo"
mkdir -p "$DEMO_HOME" "$REPO"

cleanup() {
  status=$?
  trap - EXIT INT TERM
  if [[ -x "$PREFIX/sparkwing" ]]; then
    "$PREFIX/sparkwing" daemon stop >/dev/null 2>&1 ||
      printf 'install-to-green: could not stop the admission daemon in %s\n' "$DEMO_HOME" >&2
  fi
  if ((keep)); then
    printf 'install-to-green: kept %s\n' "$WORK" >&2
  else
    # Go writes the module cache read-only, so the tree needs write permission
    # back before it can be unlinked.
    chmod -R u+w "$WORK" 2>/dev/null || true
    rm -rf "$WORK" ||
      printf 'install-to-green: could not remove %s\n' "$WORK" >&2
  fi
  exit "$status"
}
trap cleanup EXIT INT TERM

demo_env=(
  HOME="$DEMO_HOME"
  SPARKWING_HOME="$DEMO_HOME/.sparkwing"
  GOPATH="$DEMO_HOME/go"
  GOMODCACHE="$DEMO_HOME/go/pkg/mod"
  GOCACHE="$DEMO_HOME/.cache/go-build"
)

install_source=""
if [[ "$mode" == release ]]; then
  install_source="$ROOT/install/install.sh"
else
  command -v openssl >/dev/null 2>&1 || fail "openssl is required to stage a local release"
  # The installer refuses an asset whose own report disagrees with the tag it
  # was served under, so a supplied binary is staged under the tag it claims.
  if [[ "$mode" == binary ]]; then
    STAGE_TAG="$(env "${demo_env[@]}" "$binary" version -o plain --offline 2>/dev/null || true)"
    [[ "$STAGE_TAG" == v[0-9]* ]] ||
      fail "--binary $binary does not report a version tag, so no release can be staged from it"
  else
    # The scaffold pins the SDK at the version the CLI reports, so a candidate
    # build wears the newest published release tag: an invented tag pins a
    # module version the proxy cannot serve and the scaffold never finishes.
    # Prereleases and local candidate tags are excluded, because one of those
    # ranking above the published release pins a module nothing has published.
    STAGE_TAG="$(git -C "$ROOT" tag -l 'v0.*' | grep -E '^v0\.[0-9]+\.[0-9]+$' | sort -V | tail -1)"
    [[ -n "$STAGE_TAG" ]] ||
      fail "this checkout has no vX.Y.Z release tag to stamp a candidate build with; fetch tags or pass --binary"
  fi
  STAGE="$WORK/releases/$STAGE_TAG"
  mkdir -p "$STAGE"
  asset="sparkwing-$(go env GOOS)-$(go env GOARCH)"
  # Staging runs on the caller's Go caches on purpose: warming the demo home's
  # module cache here would hand the measured phases a cache no adopter has.
  if [[ "$mode" == build ]]; then
    GOWORK=off go -C "$ROOT" build -trimpath -ldflags "-s -w -X main.Version=$STAGE_TAG" \
      -o "$STAGE/$asset" ./cmd/sparkwing ||
      fail "could not build ./cmd/sparkwing for the staged release"
  else
    cp "$binary" "$STAGE/$asset"
    chmod 0755 "$STAGE/$asset"
  fi

  # The caller nominates an openssl for the installer, and staging needs the
  # same one: a system openssl that cannot mint an ed25519 key stops the run
  # here, before the nominated binary is ever consulted.
  ssl="${SPARKWING_OPENSSL:-openssl}"
  "$ssl" genpkey -algorithm ed25519 -out "$WORK/signing.pem" 2>/dev/null ||
    fail "this openssl cannot generate ed25519 keys, so no release can be staged"
  signing_key="$("$ssl" pkey -in "$WORK/signing.pem" -pubout -outform DER | tail -c 32 | "$ssl" base64 -A)"
  (cd "$STAGE" && "$ssl" dgst -sha256 -r "$asset" >SHA256SUMS)
  "$ssl" pkeyutl -sign -inkey "$WORK/signing.pem" -rawin -in "$STAGE/SHA256SUMS" -out "$STAGE/SHA256SUMS.sig"
  "$ssl" pkeyutl -sign -inkey "$WORK/signing.pem" -rawin -in "$STAGE/$asset" -out "$STAGE/$asset.sig"

  # The installer trusts the release key and takes no override, so the staged
  # copy swaps the trust root for the key minted above and nothing else.
  install_source="$WORK/install.sh"
  sed "s|^TRUSTED_PUBLIC_KEYS=.*|TRUSTED_PUBLIC_KEYS=\"$signing_key\"|" \
    "$ROOT/install/install.sh" >"$install_source"
  grep -qF "$signing_key" "$install_source" || fail "could not replace the installer trust root"
  version="$STAGE_TAG"
fi

# An inherited SPARKWING_* variable would steer the CLI away from the demo
# home, so only the openssl the caller nominated survives the reset.
caller_openssl="${SPARKWING_OPENSSL:-}"
while IFS='=' read -r inherited _; do
  case "$inherited" in
    SPARKWING_*) unset "$inherited" ;;
  esac
done < <(env)
if [[ -n "$caller_openssl" ]]; then
  export SPARKWING_OPENSSL="$caller_openssl"
fi

# Module resolution is pinned the way the caches are: an inherited proxy,
# checksum, toolchain or parallelism setting changes what the number measures,
# so each one is recorded and reset unless the caller opts in.
go_resolution_vars=(GOPROXY GONOPROXY GOPRIVATE GONOSUMDB GONOSUMCHECK GOSUMDB GOINSECURE GOVCS GOFLAGS GOTOOLCHAIN GOMAXPROCS GOCACHEPROG GOEXPERIMENT)
inherited_go_env=()
for name in "${go_resolution_vars[@]}"; do
  if [[ -n "${!name:-}" ]]; then
    inherited_go_env+=("$name")
    if ((inherit_go_env == 0)); then
      unset "$name"
    fi
  fi
done
if ((inherit_go_env == 0)); then
  export GOPROXY="https://proxy.golang.org,direct"
fi
goproxy="${GOPROXY:-https://proxy.golang.org,direct}"

export "${demo_env[@]}"
# A workspace left in the caller's tree would steer the scaffolded module's
# builds, which is not what an adopter's first compile resolves.
export GOWORK=off
unset GOBIN
# The pinned path carries the system administration directories too: this
# script reads the machine's load through a tool that lives in one of them,
# and a path without them records no load for the half of the run after it.
export PATH="$PREFIX:$GO_BIN_DIR:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
for required in git curl; do
  command -v "$required" >/dev/null 2>&1 ||
    fail "$required is required and does not resolve on the harness PATH ($PATH)"
done

if [[ "$mode" != release ]]; then
  export SPARKWING_RELEASE_BASE_URL="file://$WORK/releases"
fi

git -C "$REPO" init -q
[[ -d "$REPO/.git" ]] ||
  fail "git init created no repository under $REPO, so a git binding survived the reset"
git -C "$REPO" -c user.name=sparkwing -c user.email=demo@sparkwing.invalid \
  -c commit.gpgsign=false -c core.hooksPath=/dev/null commit -q --allow-empty -m "demo repository"

install_args=(--prefix "$PREFIX")
if [[ -n "$version" ]]; then
  install_args+=(--version "$version")
fi

start_ns="$(now_ns)"
sh "$install_source" "${install_args[@]}" >"$WORK/install.log" 2>&1 ||
  { cat "$WORK/install.log" >&2; fail "the install phase failed"; }
mark="$(now_ns)"
install_ms="$(milliseconds_of $((mark - start_ns)))"

[[ -x "$PREFIX/sparkwing" ]] || fail "the installer reported success but left no binary at $PREFIX/sparkwing"

# A candidate build compiles its own SDK, not the release its tag names: the
# scaffold's require line resolves the published module and the replace then
# points the build at this worktree, so the compile phase measures the source
# under test.
case "$mode" in
  build) sdk_source="candidate source at $ROOT via a replace pin" ;;
  binary) sdk_source="released module $STAGE_TAG" ;;
  *) sdk_source="released module ${version:-latest}" ;;
esac

previous="$mark"
# In build mode the scaffold resolves the candidate's graph, not the published
# release's: GOPROXY=off leaves `pipeline new` to write the skeleton while its
# own dependency resolution finds nothing to download, the replace then points
# the module at this worktree, and the tidy that follows resolves what the
# candidate actually needs. Without that tidy a candidate that adds a
# dependency fails the compile on a missing go.sum entry.
scaffold_proxy=("GOPROXY=$goproxy")
if [[ "$mode" == build ]]; then
  scaffold_proxy=("GOPROXY=off")
fi
env "${scaffold_proxy[@]}" sparkwing pipeline new --name demo --template "$template" -C "$REPO" \
  >"$WORK/scaffold.log" 2>&1 || {
  cat "$WORK/scaffold.log" >&2
  if download_unavailable "$WORK/scaffold.log"; then
    unavailable scaffold "could not reach the module proxy"
  fi
  fail "the scaffold phase failed"
}
if [[ "$mode" == build ]]; then
  [[ -f "$REPO/.sparkwing/go.mod" ]] ||
    { cat "$WORK/scaffold.log" >&2; fail "the scaffold left no pipeline module under $REPO/.sparkwing"; }
  go -C "$REPO/.sparkwing" mod edit -replace "github.com/sparkwing-dev/sparkwing=$ROOT" ||
    fail "could not point the scaffolded module at this worktree"
  go -C "$REPO/.sparkwing" mod tidy >"$WORK/tidy.log" 2>&1 || {
    cat "$WORK/tidy.log" >&2
    if download_unavailable "$WORK/tidy.log"; then
      unavailable scaffold "could not resolve the candidate module graph"
    fi
    fail "could not resolve the scaffolded module against this worktree"
  }
fi
mark="$(now_ns)"
scaffold_ms="$(milliseconds_of $((mark - previous)))"

previous="$mark"
sparkwing pipeline explain --name demo -C "$REPO" >"$WORK/compile.log" 2>&1 || {
  cat "$WORK/compile.log" >&2
  if download_unavailable "$WORK/compile.log"; then
    unavailable compile "could not reach the module proxy"
  fi
  fail "the first-compile phase failed"
}
mark="$(now_ns)"
compile_ms="$(milliseconds_of $((mark - previous)))"

previous="$mark"
SPARKWING_LOG_FORMAT=json sparkwing run demo -C "$REPO" >"$WORK/run.log" 2>&1 ||
  { cat "$WORK/run.log" >&2; fail "the run phase failed"; }
mark="$(now_ns)"
run_ms="$(milliseconds_of $((mark - previous)))"

grep -Eq '"event":"run_finish".*"status":"success"' "$WORK/run.log" ||
  { cat "$WORK/run.log" >&2; fail "the run exited zero without recording a green run"; }

total_ms=$((install_ms + scaffold_ms + compile_ms + run_ms))

dominant=install
dominant_ms=$install_ms
for candidate in "scaffold:$scaffold_ms" "compile:$compile_ms" "run:$run_ms"; do
  if ((${candidate#*:} > dominant_ms)); then
    dominant="${candidate%%:*}"
    dominant_ms="${candidate#*:}"
  fi
done

installed_version="$(sparkwing version -o plain --offline 2>/dev/null || echo unknown)"
load_end="$(load_average)"

met=true
if [[ -n "$target_seconds" ]] && ((total_ms > target_seconds * 1000)); then
  met=false
fi

inherited_list=""
if ((${#inherited_go_env[@]} > 0)); then
  inherited_list="${inherited_go_env[*]}"
fi

if [[ "$output" == json ]]; then
  printf '{"total_seconds":%s,"phases":{"install":%s,"scaffold":%s,"compile":%s,"run":%s},' \
    "$(seconds_of "$total_ms")" "$(seconds_of "$install_ms")" "$(seconds_of "$scaffold_ms")" \
    "$(seconds_of "$compile_ms")" "$(seconds_of "$run_ms")"
  printf '"dominant_phase":"%s","mode":"%s","template":"%s","version":"%s","sdk_source":"%s","measured":true,"green":true,' \
    "$dominant" "$mode" "$template" "$(json_string "$installed_version")" "$(json_string "$sdk_source")"
  printf '"started_at":"%s","cores":%s,"load_start":%s,"load_end":%s,"goproxy":"%s","inherited_go_env":[' \
    "$started_at" "$(json_number "$cores")" "$(json_number "$load_start")" "$(json_number "$load_end")" "$(json_string "$goproxy")"
  separator=""
  for name in ${inherited_list}; do
    printf '%s"%s"' "$separator" "$name"
    separator=","
  done
  printf ']'
  if [[ -n "$target_seconds" ]]; then
    printf ',"target_seconds":%s,"target_met":%s' "$target_seconds" "$met"
  fi
  printf '}\n'
else
  printf 'install-to-green: %s seconds to the first green run\n' "$(seconds_of "$total_ms")"
  printf '  install   %8s s\n' "$(seconds_of "$install_ms")"
  printf '  scaffold  %8s s\n' "$(seconds_of "$scaffold_ms")"
  printf '  compile   %8s s\n' "$(seconds_of "$compile_ms")"
  printf '  run       %8s s\n' "$(seconds_of "$run_ms")"
  printf '  dominant phase: %s\n' "$dominant"
  printf '  mode %s, template %s, sparkwing %s\n' "$mode" "$template" "$installed_version"
  printf '  sdk: %s\n' "$sdk_source"
  printf '  started %s, %s cores, load %s at start and %s at end\n' "$started_at" "$cores" "$load_start" "$load_end"
  printf '  GOPROXY %s\n' "$goproxy"
  if [[ -n "$inherited_list" ]]; then
    if ((inherit_go_env)); then
      printf '  inherited and kept: %s\n' "$inherited_list"
    else
      printf '  inherited and reset: %s\n' "$inherited_list"
    fi
  fi
  if [[ -n "$target_seconds" ]]; then
    printf '  target %s s: %s\n' "$target_seconds" "$([[ "$met" == true ]] && echo met || echo missed)"
  fi
fi

if [[ "$met" != true ]]; then
  printf 'install-to-green: %s seconds is over the %s second target; %s is the dominant phase at %s seconds\n' \
    "$(seconds_of "$total_ms")" "$target_seconds" "$dominant" "$(seconds_of "$dominant_ms")" >&2
  exit 1
fi
