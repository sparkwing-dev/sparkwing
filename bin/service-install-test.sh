#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CASE_ROOT="$(mktemp -d)"
trap 'rm -rf "$CASE_ROOT"' EXIT

SRC="$CASE_ROOT/install"
mkdir -p "$SRC"
cp "$ROOT/install/install.sh" "$SRC/install.sh"
cp -R "$ROOT/install/macos" "$SRC/macos"
cp -R "$ROOT/install/linux" "$SRC/linux"

STUB="$CASE_ROOT/stub"
mkdir -p "$STUB"
for cmd in sparkwing-runner launchctl systemctl; do
  printf '#!/bin/sh\nexit 0\n' >"$STUB/$cmd"
  chmod +x "$STUB/$cmd"
done

fail() {
  echo "service-install-test: $1" >&2
  shift
  for f in "$@"; do cat "$f" >&2; done
  exit 1
}

run_install() {
  local home="$1"
  shift
  mkdir -p "$home"
  env -i \
    PATH="$STUB:/usr/bin:/bin" \
    HOME="$home" \
    XDG_CONFIG_HOME="$home/.config" \
    RUNNER_NAME=test-runner \
    MAX_CONCURRENT=2 \
    SPARKWING_CONTROLLER=https://ctrl.example.com \
    SPARKWING_LOGS=https://logs.example.com \
    SPARKWING_API_TOKEN=swr_good \
    "$@" \
    bash "$SRC/install.sh" --yes
}

home_bad="$CASE_ROOT/home-bad"
if run_install "$home_bad" SPARKWING_CACHE_TOKEN='x"
labels:
  - "injected"' >"$CASE_ROOT/out-bad" 2>&1; then
  fail "installer accepted a cache token carrying a double quote and a newline" "$CASE_ROOT/out-bad"
fi
grep -q "Cache token contains" "$CASE_ROOT/out-bad" \
  || fail "rejection does not name the cache token" "$CASE_ROOT/out-bad"
[ ! -e "$home_bad/.config/sparkwing/agent.yaml" ] \
  || fail "installer wrote a config despite rejecting the cache token" "$CASE_ROOT/out-bad"

home_name="$CASE_ROOT/home-name"
if run_install "$home_name" RUNNER_NAME='box" spawn_policy: "always' \
  SPARKWING_CACHE_TOKEN=swc_good >"$CASE_ROOT/out-name" 2>&1; then
  fail "installer accepted a runner name carrying a double quote" "$CASE_ROOT/out-name"
fi
grep -q "Runner name contains" "$CASE_ROOT/out-name" \
  || fail "rejection does not name the runner name" "$CASE_ROOT/out-name"

home_ok="$CASE_ROOT/home-ok"
if ! run_install "$home_ok" SPARKWING_CACHE_TOKEN=swc_good \
  >"$CASE_ROOT/out-ok" 2>&1; then
  fail "installer refused a clean configuration" "$CASE_ROOT/out-ok"
fi
config="$home_ok/.config/sparkwing/agent.yaml"
[ -f "$config" ] || fail "clean install wrote no config at $config" "$CASE_ROOT/out-ok"
grep -qx 'cache_token: "swc_good"' "$config" \
  || fail "clean install did not record the cache token" "$config"
grep -qx 'holder_prefix: "test-runner"' "$config" \
  || fail "clean install did not record the runner name" "$config"

echo "service-install-test: ok"
