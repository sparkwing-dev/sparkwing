#!/usr/bin/env bash
set -euo pipefail

fail() { echo "desktop-update: $*" >&2; exit 1; }
if (( $# < 4 )); then
  fail 'usage: desktop-update.sh <repo> <install-directory> <wait-seconds> <state-home>...'
fi
repo="$1" dest="$2" wait_seconds="$3"
shift 3
homes=("$@")
declare -A controller_environment
controller_variables=(HOME XDG_CONFIG_HOME SPARKWING_CONFIG SPARKWING_SECRETS_KEY_FILE SPARKWING_SECRETS_KEY SPARKWING_SECRETS_PREVIOUS_KEY)
for path in "$repo" "$dest" "${homes[@]}"; do
  [[ "$path" == /* ]] || fail 'repository, install directory and state homes must be absolute'
done
[[ "$wait_seconds" =~ ^[0-9]+$ ]] && (( wait_seconds <= 86400 )) || fail 'wait-seconds must be 0..86400'
for tool in git gh jq flock timeout systemctl go pnpm; do
  command -v "$tool" >/dev/null || fail "required tool missing: $tool"
done
[[ -x "$dest/sparkwing" ]] || fail "installed CLI missing: $dest/sparkwing"
exec 9>"$dest/.sparkwing-update.lock"
flock -n 9 || fail 'another desktop update holds the install lock'
stage="$(mktemp -d "$dest/.sparkwing-update.XXXXXX")"
complete=0
cleanup() {
  if (( complete )); then
    rm -rf -- "$stage"
  else
    echo "desktop-update: update failed; installed work was not cancelled; inspect $stage" >&2
  fi
}
trap cleanup EXIT

origin="$(git -C "$repo" remote get-url origin)"
git -C "$repo" clone --branch main --single-branch -- "$origin" "$stage/source"
sha="$(git -C "$stage/source" rev-parse HEAD)"
env -C "$stage/source" gh run list --workflow ci.yaml --branch main --event push --commit "$sha" \
  --limit 1 --json headSha,status,conclusion >"$stage/ci.json"
jq -e --arg sha "$sha" 'length == 1 and .[0].headSha == $sha and
  .[0].status == "completed" and .[0].conclusion == "success"' "$stage/ci.json" >/dev/null \
  || fail "main CI is absent, pending or failed at $sha; installed binaries unchanged"
GOWORK=off GOFLAGS="${GOFLAGS:-} -p=2" SKIP_WEB_BUILD=0 SPARKWING_INSTALL_NAME=sparkwing \
  SPARKWING_INSTALL_BIN="$stage/bin" bash "$stage/source/bin/install.sh"
for name in sparkwing sparkwing-runner; do
  [[ -f "$stage/bin/$name" && -x "$stage/bin/$name" && ! -L "$stage/bin/$name" ]] || fail "build produced no regular executable: $name"
done

inspect() {
  busy=0
  restart_homes=()
  restart_pids=()
  for home in "${homes[@]}"; do
    SPARKWING_HOME="$home" timeout 10s "$dest/sparkwing" daemon status -o json >"$stage/status.json" \
      || fail "cannot inspect controller at $home; installed binaries unchanged"
    jq -e '.kind == "service" and .tool == "sparkwing" and .service == "controller" and
      (.state == "running" or .state == "stopped") and
      ([.activity.runs, .activity.queued_triggers, .activity.held_leases] |
        all(type == "number" and . >= 0 and floor == .))' "$stage/status.json" >/dev/null \
      || fail "invalid controller status at $home; installed binaries unchanged"
    if jq -e '.state == "running"' "$stage/status.json" >/dev/null; then
      restart_homes+=("$home")
      restart_pids+=("$(jq -er '.pid | select(type == "number" and . > 0 and floor == .)' "$stage/status.json")") \
        || fail "invalid controller PID at $home; installed binaries unchanged"
    fi
    if ! jq -e '[.activity.runs, .activity.queued_triggers, .activity.held_leases] | all(. == 0)' \
      "$stage/status.json" >/dev/null; then
      echo "desktop-update: waiting for runs, triggers and leases at $home" >&2
      printf 'desktop-update: inspect: SPARKWING_HOME=%q %q runs list --profile= --status pending,running --since 0 -o json\n' "$home" "$dest/sparkwing" >&2
      if SPARKWING_HOME="$home" timeout 10s "$dest/sparkwing" runs list --profile= \
        --status pending,running --since 0 --limit 5 -o json >"$stage/runs.json"; then
        jq -sr 'map(select(.id != null))[:5][] | "desktop-update: blocking run \(.id) (\(.pipeline), \(.status))"' \
          "$stage/runs.json" >&2 || echo "desktop-update: blocking run details unavailable at $home" >&2
        total="$(jq -r '.activity.runs' "$stage/status.json")"
        echo "desktop-update: $total active runs at $home (showing up to 5)" >&2
      else
        echo "desktop-update: blocking run details unavailable at $home" >&2
      fi
      busy=1
    fi
  done
  timeout 10s systemctl --user list-units --type=service --state=active,activating,deactivating,reloading \
    --output=json >"$stage/units.json" || fail 'cannot inspect user units; installed binaries unchanged'
  jq -e 'type == "array" and all(.[]; (.unit | type == "string"))' "$stage/units.json" >/dev/null \
    || fail 'invalid user-unit list; installed binaries unchanged'
  jq -r '.[].unit' "$stage/units.json" >"$stage/units.txt"
  while IFS= read -r unit; do
    command_line="$(timeout 10s systemctl --user show --property=ExecStart --value "$unit")" \
      || fail "cannot inspect $unit; installed binaries unchanged"
    if [[ "$unit" == *land* ]] || [[ "$command_line" =~ sparkwing[[:space:]]+run[[:space:]]+land([[:space:];]|$) ]]; then
      echo "desktop-update: waiting for $unit" >&2
      busy=1
    fi
  done <"$stage/units.txt"
}

deadline=$(( SECONDS + wait_seconds ))
while true; do
  inspect
  if (( ! busy )); then break; fi
  (( SECONDS < deadline )) || fail "drain timed out after ${wait_seconds}s; installed binaries unchanged"
  sleep 2
done

# safety: pipeline environments omit key selectors; each live owner is the source for its replacement.
for i in "${!restart_homes[@]}"; do
  home="${restart_homes[$i]}"
  while IFS= read -r -d '' entry; do
    name="${entry%%=*}"
    case "$name" in
      HOME|XDG_CONFIG_HOME|SPARKWING_CONFIG|SPARKWING_SECRETS_KEY_FILE|SPARKWING_SECRETS_KEY|SPARKWING_SECRETS_PREVIOUS_KEY)
        controller_environment["$home:$name"]="${entry#*=}" ;;
    esac
  done <"/proc/${restart_pids[$i]}/environ" || fail "cannot read controller environment at $home; installed binaries unchanged"
  [[ -n "${controller_environment["$home:HOME"]:-}" ]] || fail "missing controller HOME at $home; installed binaries unchanged"
done

echo "desktop-update: idle; installing main@$sha"
cp -p -- "$dest/sparkwing-runner" "$stage/previous-runner"
mv -f -- "$stage/bin/sparkwing-runner" "$dest/sparkwing-runner"
if ! mv -f -- "$stage/bin/sparkwing" "$dest/sparkwing"; then
  mv -f -- "$stage/previous-runner" "$dest/sparkwing-runner" \
    || fail "CLI swap and runner rollback failed; mixed versions installed; restore $stage/previous-runner to $dest/sparkwing-runner before retrying"
  fail 'CLI swap failed; previous binaries restored; retry the desktop update'
fi
for home in "${restart_homes[@]}"; do
  (
    for name in "${controller_variables[@]}"; do
      if [[ "${controller_environment["$home:$name"]+set}" ]]; then
        export "$name=${controller_environment["$home:$name"]}"
      else
        unset "$name"
      fi
    done
    SPARKWING_HOME="$home" timeout 40s "$dest/sparkwing" daemon restart --force -o json
  )
done
complete=1
echo "desktop-update: installed main@$sha"
