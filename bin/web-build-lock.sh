#!/usr/bin/env bash

sparkwing_lock_web_build() {
  local root="$1" state lock inherited
  SPARKWING_WEB_BUILD_LOCKED=0
  if ! command -v flock >/dev/null 2>&1; then
    return 0
  fi
  state="$root/internal/web/.build-state"
  lock="$state/lock"
  if [[ -L "$state" || -L "$lock" ]]; then
    echo "web build state must not be symlinked" >&2
    return 1
  fi
  mkdir -p "$state"
  chmod 700 "$state"
  inherited="${SPARKWING_WEB_BUILD_LOCK_FD:-}"
  if [[ "$inherited" =~ ^[0-9]+$ && "/dev/fd/$inherited" -ef "$lock" ]]; then
    flock -x "$inherited"
  else
    # safety: keep the descriptor open through the installer's Go compile.
    exec 9>"$lock"
    chmod 600 "$lock"
    flock -x 9
    export SPARKWING_WEB_BUILD_LOCK_FD=9
  fi
  SPARKWING_WEB_BUILD_LOCKED=1
}
