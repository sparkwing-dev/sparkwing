#!/usr/bin/env bash

sparkwing_lock_web_build() {
  local root="$1" state lock inherited compatible_bash owner argument program encoded
  shift
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
    case "$(uname -s)" in
      # hack: MSYS flock releases a re-acquired parent lock on child exit.
      MINGW*|MSYS*|CYGWIN*)
        owner="${SPARKWING_WEB_BUILD_LOCK_OWNER:-}"
        if [[ "$owner" =~ ^[0-9]+$ && ( "$owner" == "$PPID" || "$owner" == "$$" ) ]] && kill -0 "$owner" 2>/dev/null; then
          SPARKWING_WEB_BUILD_LOCKED=1
          return 0
        fi
        ;;
      *)
        flock -x "$inherited"
        SPARKWING_WEB_BUILD_LOCKED=1
        return 0
        ;;
    esac
  fi
  # safety: keep the descriptor open through the installer's Go compile.
  exec 9>"$lock"
  chmod 600 "$lock"
  if ! flock -x 9; then
    case "$(uname -s)" in
      MINGW*|MSYS*|CYGWIN*)
        # hack: Windows POSIX runtimes cannot share inherited descriptors.
        compatible_bash="$(dirname "$(command -v flock)")/bash.exe"
        if [[ -x "$compatible_bash" && "${SPARKWING_WEB_BUILD_LOCK_REEXEC:-0}" != 1 ]]; then
          export SPARKWING_WEB_BUILD_LOCK_REEXEC=1
          # hack: a native bridge preserves exports across different MSYS runtimes.
          argument="$(cygpath -m "$compatible_bash")"
          program="& '${argument//\'/\'\'}'"
          for argument in "$(cygpath -m "$0")" "$@"; do
            program+=" '${argument//\'/\'\'}'"
          done
          program+='; exit $LASTEXITCODE'
          encoded="$(printf '%s' "$program" | iconv -f UTF-8 -t UTF-16LE | base64 -w 0)"
          exec powershell.exe -NoProfile -NonInteractive -EncodedCommand "$encoded"
        fi
        ;;
    esac
    echo "web build lock failed; a compatible Bash and flock runtime is required" >&2
    return 1
  fi
  export SPARKWING_WEB_BUILD_LOCK_FD=9 SPARKWING_WEB_BUILD_LOCK_OWNER="$$"
  SPARKWING_WEB_BUILD_LOCKED=1
}
