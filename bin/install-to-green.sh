#!/usr/bin/env bash
# Times the demo path end to end: install the CLI the way an adopter does,
# scaffold a pipeline, compile it, and run it to green. Prints the seconds from
# install to first green run, with each phase broken out.
#
# Every phase starts from a clean HOME, so the Go module cache, the Go build
# cache and the sparkwing home are all empty: the number covers the dependency
# download an adopter actually waits for.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

usage() {
  cat <<'EOF'
usage: bash bin/install-to-green.sh [flags]

  --build              Build cmd/sparkwing from this checkout and install that
                       as a locally staged, signed release (default).
  --binary PATH        Install PATH as the staged release asset instead of
                       building one.
  --release            Install from the published release over the network,
                       which is the documented install path verbatim.
  --version TAG        Release tag for --release (default: latest).
  --template SHAPE     Pipeline template to scaffold (default: minimal).
  --target-seconds N   Exit non-zero when the total exceeds N seconds.
  --output FORMAT      pretty (default) or json.
  --keep               Leave the scratch tree behind and print its path.
  -h, --help           This message.

Local mode stages the asset on disk and serves it over file://, so the install
phase measures verification and placement, not the network transfer. Use
--release for a number that includes the download.
EOF
}

fail() {
  printf 'install-to-green: %s\n' "$*" >&2
  exit 1
}

mode=build
binary=""
version=""
template=minimal
target_seconds=""
output=pretty
keep=0

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

seconds_of() {
  printf '%d.%03d' "$(($1 / 1000000000))" "$((($1 / 1000000) % 1000))"
}

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
  if [[ -x "$PREFIX/sparkwing" ]]; then
    "$PREFIX/sparkwing" daemon stop >/dev/null 2>&1 ||
      printf 'install-to-green: no admission daemon to stop in %s\n' "$DEMO_HOME" >&2
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

install_source=""
if [[ "$mode" == release ]]; then
  install_source="$ROOT/install/install.sh"
else
  command -v openssl >/dev/null 2>&1 || fail "openssl is required to stage a local release"
  # The installer refuses an asset whose own report disagrees with the tag it
  # was served under, so a supplied binary is staged under the tag it claims.
  if [[ "$mode" == binary ]]; then
    STAGE_TAG="$("$binary" version -o plain --offline 2>/dev/null || true)"
    [[ "$STAGE_TAG" == v[0-9]* ]] ||
      fail "--binary $binary does not report a version tag, so no release can be staged from it"
  else
    # The scaffold pins the SDK at the version the CLI reports, so a candidate
    # build is stamped with the newest released tag: an invented tag pins a
    # module version the proxy cannot serve and the first compile never starts.
    STAGE_TAG="$(git -C "$ROOT" tag -l 'v0.*' | sort -V | tail -1)"
    [[ -n "$STAGE_TAG" ]] ||
      fail "this checkout has no v0.* release tag to stamp a candidate build with; fetch tags or pass --binary"
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

  openssl genpkey -algorithm ed25519 -out "$WORK/signing.pem" 2>/dev/null ||
    fail "this openssl cannot generate ed25519 keys, so no release can be staged"
  signing_key="$(openssl pkey -in "$WORK/signing.pem" -pubout -outform DER | tail -c 32 | openssl base64 -A)"
  (cd "$STAGE" && openssl dgst -sha256 -r "$asset" >SHA256SUMS)
  openssl pkeyutl -sign -inkey "$WORK/signing.pem" -rawin -in "$STAGE/SHA256SUMS" -out "$STAGE/SHA256SUMS.sig"
  openssl pkeyutl -sign -inkey "$WORK/signing.pem" -rawin -in "$STAGE/$asset" -out "$STAGE/$asset.sig"

  # The installer trusts the release key and takes no override, so the staged
  # copy swaps the trust root and nothing else.
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

export HOME="$DEMO_HOME"
export SPARKWING_HOME="$DEMO_HOME/.sparkwing"
export GOPATH="$DEMO_HOME/go"
export GOMODCACHE="$DEMO_HOME/go/pkg/mod"
export GOCACHE="$DEMO_HOME/.cache/go-build"
# A workspace left in the caller's tree would steer the scaffolded module's
# builds, which is not what an adopter's first compile resolves.
export GOWORK=off
unset GOFLAGS GOBIN
export PATH="$PREFIX:$GO_BIN_DIR:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
for required in git curl; do
  command -v "$required" >/dev/null 2>&1 ||
    fail "$required is required and does not resolve on the harness PATH ($PATH)"
done

if [[ "$mode" != release ]]; then
  export SPARKWING_RELEASE_BASE_URL="file://$WORK/releases"
fi

git -C "$REPO" init -q
git -C "$REPO" -c user.name=sparkwing -c user.email=demo@sparkwing.invalid \
  -c commit.gpgsign=false -c core.hooksPath=/dev/null commit -q --allow-empty -m "demo repository"

install_args=(--prefix "$PREFIX")
if [[ -n "$version" ]]; then
  install_args+=(--version "$version")
fi

phase_install=0
phase_scaffold=0
phase_compile=0
phase_run=0

started="$(now_ns)"
sh "$install_source" "${install_args[@]}" >"$WORK/install.log" 2>&1 ||
  { cat "$WORK/install.log" >&2; fail "the install phase failed"; }
mark="$(now_ns)"
phase_install=$((mark - started))

[[ -x "$PREFIX/sparkwing" ]] || fail "the installer reported success but left no binary at $PREFIX/sparkwing"

sparkwing pipeline new --name demo --template "$template" -C "$REPO" >"$WORK/scaffold.log" 2>&1 ||
  { cat "$WORK/scaffold.log" >&2; fail "the scaffold phase failed"; }
previous="$mark"
mark="$(now_ns)"
phase_scaffold=$((mark - previous))

sparkwing pipeline explain --name demo -C "$REPO" >"$WORK/compile.log" 2>&1 ||
  { cat "$WORK/compile.log" >&2; fail "the first-compile phase failed"; }
previous="$mark"
mark="$(now_ns)"
phase_compile=$((mark - previous))

SPARKWING_LOG_FORMAT=json sparkwing run demo -C "$REPO" >"$WORK/run.log" 2>&1 ||
  { cat "$WORK/run.log" >&2; fail "the run phase failed"; }
previous="$mark"
mark="$(now_ns)"
phase_run=$((mark - previous))

grep -q '"event":"run_finish"' "$WORK/run.log" && grep -q '"status":"success"' "$WORK/run.log" ||
  { cat "$WORK/run.log" >&2; fail "the run exited zero without recording a green run"; }

total=$((phase_install + phase_scaffold + phase_compile + phase_run))

dominant=install
dominant_ns=$phase_install
for candidate in "scaffold:$phase_scaffold" "compile:$phase_compile" "run:$phase_run"; do
  if ((${candidate#*:} > dominant_ns)); then
    dominant="${candidate%%:*}"
    dominant_ns="${candidate#*:}"
  fi
done

installed_version="$(sparkwing version -o plain --offline 2>/dev/null || echo unknown)"

met=true
if [[ -n "$target_seconds" ]] && ((total > target_seconds * 1000000000)); then
  met=false
fi

if [[ "$output" == json ]]; then
  printf '{"total_seconds":%s,"phases":{"install":%s,"scaffold":%s,"compile":%s,"run":%s},' \
    "$(seconds_of "$total")" "$(seconds_of "$phase_install")" "$(seconds_of "$phase_scaffold")" \
    "$(seconds_of "$phase_compile")" "$(seconds_of "$phase_run")"
  printf '"dominant_phase":"%s","mode":"%s","template":"%s","version":"%s","green":true' \
    "$dominant" "$mode" "$template" "$installed_version"
  if [[ -n "$target_seconds" ]]; then
    printf ',"target_seconds":%s,"target_met":%s' "$target_seconds" "$met"
  fi
  printf '}\n'
else
  printf 'install-to-green: %s seconds to the first green run\n' "$(seconds_of "$total")"
  printf '  install   %8s s\n' "$(seconds_of "$phase_install")"
  printf '  scaffold  %8s s\n' "$(seconds_of "$phase_scaffold")"
  printf '  compile   %8s s\n' "$(seconds_of "$phase_compile")"
  printf '  run       %8s s\n' "$(seconds_of "$phase_run")"
  printf '  dominant phase: %s\n' "$dominant"
  printf '  mode %s, template %s, sparkwing %s\n' "$mode" "$template" "$installed_version"
  if [[ -n "$target_seconds" ]]; then
    printf '  target %s s: %s\n' "$target_seconds" "$([[ "$met" == true ]] && echo met || echo missed)"
  fi
fi

if [[ "$met" != true ]]; then
  printf 'install-to-green: %s seconds is over the %s second target; %s is the dominant phase at %s seconds\n' \
    "$(seconds_of "$total")" "$target_seconds" "$dominant" "$(seconds_of "$dominant_ns")" >&2
  exit 1
fi
