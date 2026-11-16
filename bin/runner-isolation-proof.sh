#!/usr/bin/env bash
# The runner user writes the evidence, so redirects stay outside sudo (SC2024);
# functions reached through setup, check and deadline read as unreachable (SC2317);
# single-quoted jq programs expand their own variables (SC2016).
# shellcheck disable=SC2024,SC2317,SC2016
#
# Live isolation proof for `sparkwing runners add --service-user`, run only by
# .github/workflows/runner-isolation-proof.yaml on a GitHub-hosted VM that is
# discarded after the job. It creates OS users, system services and sudoers
# entries and never undoes the sudoers or fixture changes.
#
# Usage: bin/runner-isolation-proof.sh EVIDENCE_DIR
#        bin/runner-isolation-proof.sh --preflight [FACTS_ROOT]
#
# Before any write it requires two host facts that no environment variable
# supplies: GitHub's root-owned runner-image record, and the Actions
# Runner.Worker process among its own ancestors. GITHUB_ACTIONS and
# RUNNER_ENVIRONMENT are checked first as a filter only; they prove nothing.
# A self-hosted runner built from GitHub's runner image would pass, so the
# workflow pins a GitHub-hosted label. --preflight runs only these checks,
# reading the facts under FACTS_ROOT (default /), writes nothing, and exits 0
# when the proof would run or 2 when it refuses.
#
# Every check prints "PASS name", "FAIL name (why)" or "INFO note"; the last
# line is "RESULT: PASS" or "RESULT: FAIL (n failed checks)". A setup step
# that fails stops the proof with "RESULT: FAIL (setup: ...)". Exit 0 only on
# PASS.
set -uo pipefail

IMAGE_RECORD=/imagegeneration/imagedata.json

# hosted_vm ROOT: prints each fact and succeeds only when every one holds.
hosted_vm() {
  local root=$1 ok=0 pid comm parent found="" record="$1$IMAGE_RECORD"
  if [[ "${GITHUB_ACTIONS:-}" == true && "${RUNNER_ENVIRONMENT:-}" == github-hosted ]]; then
    echo "filter: GITHUB_ACTIONS=true and RUNNER_ENVIRONMENT=github-hosted"
  else
    echo "refused: GITHUB_ACTIONS is not true or RUNNER_ENVIRONMENT is not github-hosted"
    ok=1
  fi
  if [[ -f "$record" && ! -L "$record" && "$(stat -c %u "$record" 2>/dev/null)" == 0 && ! -w "$record" ]]; then
    echo "fact: $IMAGE_RECORD is a root-owned file this user cannot write"
  else
    echo "refused: no root-owned $IMAGE_RECORD, the record GitHub's hosted runner images carry"
    ok=1
  fi
  pid=$$
  while [[ -n "$pid" && "$pid" != 0 && "$pid" != 1 ]]; do
    comm="$(cat "$root/proc/$pid/comm" 2>/dev/null)"
    if [[ "$comm" == Runner.Worker ]]; then
      found=$pid
      break
    fi
    parent="$(awk '/^PPid:/{print $2}' "$root/proc/$pid/status" 2>/dev/null)"
    pid=$parent
  done
  if [[ -n "$found" ]]; then
    echo "fact: ancestor process $found is the Actions Runner.Worker"
  else
    echo "refused: no Runner.Worker process among this script's ancestors"
    ok=1
  fi
  return "$ok"
}

refuse() {
  echo "runner-isolation-proof: refusing: this creates OS users, sudoers entries and system services, and runs only on a discarded GitHub-hosted VM" >&2
  echo "RESULT: REFUSED"
  exit 2
}

if [[ "${1:-}" == --preflight ]]; then
  if hosted_vm "${2:-}"; then
    echo "RESULT: WOULD RUN"
    exit 0
  fi
  refuse
fi
hosted_vm "" || refuse
if [[ $# -ne 1 || -z "$1" ]]; then
  echo "usage: $0 EVIDENCE_DIR" >&2
  exit 2
fi

EVIDENCE=$1
REPO="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${RUNNER_TEMP:?}/runner-isolation-work"
PORT=18999
CONTROLLER="http://127.0.0.1:$PORT"
TEAM=proof
SHARED_ACCOUNT=sw-proof-shared
SHARED_RUNNER=proof-shared
JOB_ACCOUNT=sw-proof-job
JOB_RUNNER=proof-job
REPOSITORY=example.com/proof/e2e
HOME_ROOT=/var/lib/sparkwing
FAILURES=0
CONTROLLER_PID=""

mkdir -p "$EVIDENCE" "$WORK"
SUMMARY="$EVIDENCE/checks.txt"
: >"$SUMMARY"
hosted_vm "" >"$EVIDENCE/hosted-vm-facts.txt"

note() { echo "== $*"; }
record() { echo "$1" | tee -a "$SUMMARY"; }
pass() { record "PASS $1"; }
fail() { record "FAIL $1${2:+ ($2)}"; FAILURES=$((FAILURES + 1)); }
info() { record "INFO $1"; }
expect() { if [[ "$2" == "$3" ]]; then pass "$1"; else fail "$1" "got '$2', want '$3'"; fi; }
expect_not() { if [[ "$2" != "$3" ]]; then pass "$1"; else fail "$1" "got '$2'"; fi; }
check() { local name=$1; shift; if "$@"; then pass "$name"; else fail "$name"; fi; }
mask() { [[ -n "$1" ]] && echo "::add-mask::$1"; }

finish() {
  local code=$?
  if [[ -n "$CONTROLLER_PID" ]] && kill -0 "$CONTROLLER_PID" 2>/dev/null; then
    kill "$CONTROLLER_PID" 2>/dev/null
  fi
  if [[ -s "$SUMMARY" ]]; then
    {
      echo "### Runner isolation proof"
      echo
      echo '```'
      cat "$SUMMARY"
      echo '```'
    } >>"${GITHUB_STEP_SUMMARY:-/dev/null}"
  fi
  exit "$code"
}
trap finish EXIT

setup() {
  if ! "$@"; then
    record "FAIL setup: $*"
    record "RESULT: FAIL (setup: $1)"
    exit 1
  fi
}

# deadline SECONDS CMD...: retries CMD once a second until it succeeds or SECONDS pass.
deadline() {
  local until=$((SECONDS + $1))
  shift
  while ((SECONDS < until)); do
    if "$@"; then return 0; fi
    sleep 1
  done
  return 1
}

api() { curl --fail --silent --show-error --max-time 10 -H "Authorization: Bearer $TEAM_TOKEN" "$@"; }
# api_status BEARER PATH: prints the HTTP status, or curl-exit-N when no HTTP exchange completed.
api_status() {
  local code
  code="$(curl --silent --output /dev/null --max-time 10 --write-out '%{http_code}' -H "Authorization: Bearer $1" "$CONTROLLER$2")" || {
    echo "curl-exit-$?"
    return
  }
  echo "$code"
}

ALICE_PATH=""
as_alice() { sudo -u alice -- env -i -C / HOME=/home/alice USER=alice LOGNAME=alice PATH="$ALICE_PATH" "$@"; }
as_account() {
  local account=$1
  shift
  sudo -u "$account" -- env -i -C / HOME="$HOME_ROOT/$account" USER="$account" LOGNAME="$account" PATH=/usr/local/bin:/usr/bin:/bin "$@"
}

unit_of() { echo "sparkwing-runner-$1.service"; }
unit_file_of() { echo "/etc/systemd/system/$(unit_of "$1")"; }

note "environment"
{
  echo "commit=$(git -C "$REPO" rev-parse HEAD)"
  echo "kernel=$(uname -srm)"
  echo "systemd=$(systemctl --version | head -n1)"
  grep -E '^(PRETTY_NAME|VERSION_ID)=' /etc/os-release
  echo "runner_user=$(id -un) uid=$(id -u)"
  echo "go=$(go version)"
} | tee "$EVIDENCE/environment.txt"
setup test -d /run/systemd/system
setup sudo -n true

note "build the CLI and runner from this commit, and a controller that trusts this run's license key"
VERSION="v0.0.0-dev+$(git -C "$REPO" rev-parse --short=12 HEAD)"
BIN="$WORK/bin"
mkdir -p "$BIN" "$WORK/license"
setup go -C "$REPO" build -trimpath -ldflags "-X main.Version=$VERSION" -o "$BIN/sparkwing" ./cmd/sparkwing
setup go -C "$REPO" build -trimpath -ldflags "-X main.Version=$VERSION" -o "$BIN/sparkwing-runner" ./cmd/sparkwing-runner
# The controller is a stand-in for a licensed deployment: only its embedded license key is replaced, through an overlay, so the checkout stays unmodified.
setup openssl genpkey -algorithm ed25519 -out "$WORK/license/key.pem"
setup sh -c "openssl pkey -in '$WORK/license/key.pem' -pubout -outform DER | tail -c 32 | base64 >'$WORK/license/license_key.pub'"
printf '{"Replace":{"%s":"%s"}}\n' "$REPO/internal/license/license_key.pub" "$WORK/license/license_key.pub" >"$WORK/license/overlay.json"
setup go -C "$REPO" build -trimpath -overlay "$WORK/license/overlay.json" -o "$BIN/sparkwing-controller-proof" ./cmd/sparkwing-controller
setup sudo install -m 0755 -o root -g root "$BIN/sparkwing" "$BIN/sparkwing-runner" /usr/local/bin/
expect "checkout unmodified by the build" "$(git -C "$REPO" status --porcelain --untracked-files=no)" ""
sha256sum "$BIN"/* >"$EVIDENCE/binaries.sha256"
/usr/local/bin/sparkwing version -o json >"$EVIDENCE/cli-version.json" 2>&1 || true

note "stand-in licensed controller (team deployment, SQLite, loopback only)"
CTL="$WORK/controller"
mkdir -p "$CTL/home" "$CTL/state" "$CTL/credentials"
chmod 0700 "$CTL/credentials"
BOOTSTRAP="swk_$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')"
mask "$BOOTSTRAP"
(umask 077 && printf '%s' "$BOOTSTRAP" >"$CTL/credentials/bootstrap-admin-token" && openssl rand 32 >"$CTL/credentials/secrets-key")
ctl_env() { env HOME="$CTL/home" SPARKWING_HOME="$CTL/state" "$@"; }
controller_up() { curl --fail --silent --max-time 2 "$CONTROLLER/api/v1/health" >/dev/null; }
port_listeners() { ss -ltnpH "( sport = :$PORT )" 2>/dev/null; }
port_free() { ! controller_up && [[ -z "$(port_listeners)" ]]; }
# controller_serving: health answers, and the listener on the port belongs to CONTROLLER_PID.
controller_serving() { controller_up && [[ "$(port_listeners)" == *"pid=$CONTROLLER_PID,"* ]]; }
# controller_exited: the process is gone or a zombie awaiting wait.
controller_exited() {
  local state
  state="$(awk '{print $3}' "/proc/$CONTROLLER_PID/stat" 2>/dev/null)"
  [[ -z "$state" || "$state" == Z ]]
}
start_controller() {
  # safety: health from a listener already on the port would not be this controller's.
  if ! port_free; then
    echo "port $PORT is already in use before start" >&2
    return 1
  fi
  # safety: env execs the controller in the background child, so $! is the controller itself.
  # Backgrounding a shell function makes $! a subshell, and killing it leaves the controller running.
  env HOME="$CTL/home" SPARKWING_HOME="$CTL/state" "$BIN/sparkwing-controller-proof" \
    --addr "127.0.0.1:$PORT" --credentials-dir "$CTL/credentials" --deployment "$1" >>"$EVIDENCE/controller.log" 2>&1 &
  CONTROLLER_PID=$!
  deadline 30 controller_serving || return 1
  if [[ "$(readlink "/proc/$CONTROLLER_PID/exe")" != "$BIN/sparkwing-controller-proof" ]]; then
    echo "pid $CONTROLLER_PID is not the controller binary" >&2
    return 1
  fi
}
stop_controller() {
  kill "$CONTROLLER_PID" || return 1
  deadline 30 controller_exited || return 1
  wait "$CONTROLLER_PID" 2>/dev/null
  deadline 30 port_free
}
# Team mode refuses the deployment bearer, so the team is created in local mode first, as bin/k8s-e2e.sh does.
setup start_controller local
create_team() {
  curl --fail --silent --show-error --max-time 10 -H "Authorization: Bearer $BOOTSTRAP" -H 'Content-Type: application/json' \
    --data "{\"slug\":\"$TEAM\",\"display_name\":\"Isolation proof\"}" "$CONTROLLER/api/v1/operator/teams" >"$WORK/team.json"
}
setup create_team
TEAM_TOKEN="$(jq -r .capability "$WORK/team.json")"
ACCOUNT_ID="$(jq -r .account_id "$WORK/team.json")"
mask "$TEAM_TOKEN"
setup test -n "$TEAM_TOKEN" -a -n "$ACCOUNT_ID"
setup stop_controller
NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
EXPIRES="$(date -u -d '+1 day' +%Y-%m-%dT%H:%M:%SZ)"
printf '{"features":["multi-team"],"issued_to":"runner-isolation-proof","issued_at":"%s","expires_at":"%s"}' "$NOW" "$EXPIRES" >"$WORK/license/payload.json"
setup openssl pkeyutl -sign -rawin -inkey "$WORK/license/key.pem" -in "$WORK/license/payload.json" -out "$WORK/license/signature"
b64url() { base64 -w0 | tr '+/' '-_' | tr -d '='; }
(umask 077 && printf '%s.%s' "$(b64url <"$WORK/license/payload.json")" "$(b64url <"$WORK/license/signature")" >"$CTL/credentials/license")
setup start_controller team
admin_session() {
  ctl_env "$BIN/sparkwing-controller-proof" admin session --operator "$ACCOUNT_ID" --team "$TEAM" --reason "$1" --output plain 2>>"$EVIDENCE/admin-session.log"
}

note "the teammate: alice, a daily-driver login with sudo, holding fake secrets"
setup sudo useradd --create-home --shell /bin/bash alice
setup sudo sh -c "printf 'alice ALL=(ALL) NOPASSWD:ALL\n' >/etc/sudoers.d/90-alice && chmod 0440 /etc/sudoers.d/90-alice"
# The home is world-traversable so the uid-only probe can show what ProtectHome adds.
setup sudo chmod 0755 /home/alice
setup sudo install -d -m 0700 -o alice -g alice /home/alice/.ssh /home/alice/.config
setup sudo install -d -m 0755 -o alice -g alice /home/alice/.aws
setup sudo sh -c 'printf "FAKE-SSH-KEY-NOT-REAL\n" | install -m 0600 -o alice -g alice /dev/stdin /home/alice/.ssh/id_ed25519'
setup sudo sh -c 'printf "[default]\naws_secret_access_key = FAKE\n" | install -m 0600 -o alice -g alice /dev/stdin /home/alice/.aws/credentials'
setup sudo sh -c 'printf "notes\n" | install -m 0644 -o alice -g alice /dev/stdin /home/alice/notes.txt'
setup sudo sh -c 'printf "alice tmp\n" | install -m 0644 -o alice -g alice /dev/stdin /tmp/alice-shared-tmp-marker'
for spec in "/home/alice 755" "/home/alice/.ssh 700" "/home/alice/.ssh/id_ed25519 600" "/home/alice/.aws/credentials 600" "/home/alice/notes.txt 644" "/tmp/alice-shared-tmp-marker 644"; do
  expect "fixture ${spec% *} is alice's, mode ${spec#* }" "$(sudo stat -c '%U %a' "${spec% *}")" "alice ${spec#* }"
done
((FAILURES == 0)) || { record "RESULT: FAIL (fixture)"; exit 1; }
ALICE_PATH="/usr/local/bin:$(dirname "$(command -v go)"):/usr/bin:/bin:/usr/sbin:/sbin"
add_profile() {
  printf '%s' "$TEAM_TOKEN" | as_alice sparkwing profiles add --profile work --controller "$CONTROLLER" --token-stdin -o json >"$EVIDENCE/profile.json"
}
setup add_profile

# enroll NAME ACCOUNT FLAGS...: runs the real runners add --service-user as alice with a fresh break-glass session.
enroll() {
  local runner=$1 account=$2 session
  shift 2
  session="$(admin_session "isolation proof: enroll $runner")" || return 1
  mask "$session"
  printf '%s' "$session" | as_alice sparkwing runners add --profile work --runner "$runner" --session-stdin --service-user "$account" "$@" -o json \
    >"$EVIDENCE/add-$runner.json" 2>"$EVIDENCE/add-$runner.err"
}

runtime_uid() {
  local pid
  pid="$(systemctl show "$(unit_of "$1")" -p MainPID --value)"
  [[ -n "$pid" && "$pid" != 0 ]] || return 1
  awk '/^Uid:/{print $2}' "/proc/$pid/status"
}

runner_seen() { api "$CONTROLLER/api/v1/runners" | jq -e --arg name "$1" '.runners | any(.name == $name and .last_seen != null)' >/dev/null; }
runner_last_seen() { api "$CONTROLLER/api/v1/runners" | jq -r --arg name "$1" '.runners[] | select(.name == $name) | .last_seen'; }

# account_checks RUNNER ACCOUNT: the account, its files and the running unit.
account_checks() {
  local runner=$1 account=$2 entry account_uid uid
  local add="$EVIDENCE/add-$runner.json" unit unit_file
  unit="$(unit_of "$account")"
  unit_file="$(unit_file_of "$account")"
  check "$runner: install-service created $account" jq -e '.service_user.created == true' "$add"
  entry="$(getent passwd "$account" || true)"
  check "$runner: account exists" test -n "$entry"
  expect "$runner: account home" "$(cut -d: -f6 <<<"$entry")" "$HOME_ROOT/$account"
  expect "$runner: account shell" "$(cut -d: -f7 <<<"$entry")" /usr/sbin/nologin
  account_uid="$(id -u "$account" 2>/dev/null || echo none)"
  expect "$runner: install result names the account uid" "$(jq -r .service_user.account.uid "$add")" "$account_uid"
  case " $(id -nG "$account" 2>/dev/null) " in
    *" sudo "* | *" docker "* | *" root "* | *" adm "* | *" lxd "*) fail "$runner: account holds no privileged group" "$(id -nG "$account")" ;;
    *) pass "$runner: account holds no privileged group" ;;
  esac
  expect "$runner: account home mode and owner" "$(sudo stat -c '%a %U' "$HOME_ROOT/$account")" "700 $account"
  expect "$runner: runner credential owner and mode" "$(sudo find "$HOME_ROOT/$account" -name agent-token -printf '%u %m\n')" "$account 600"
  if sudo grep -q '^runner:' /home/alice/.config/sparkwing/config.yaml 2>/dev/null; then fail "$runner: alice's config holds no runner section"; else pass "$runner: alice's config holds no runner section"; fi
  if sudo find /home/alice -name agent-token | grep -q .; then fail "$runner: no runner credential under alice's home"; else pass "$runner: no runner credential under alice's home"; fi
  expect "$runner: root-owned enrollment record" "$(sudo stat -c '%U %a' "$HOME_ROOT/.enrollments/$account.json" 2>&1)" "root 600"
  expect "$runner: unit User" "$(systemctl show "$unit" -p User --value)" "$account"
  expect "$runner: unit ProtectHome" "$(systemctl show "$unit" -p ProtectHome --value)" yes
  expect "$runner: unit NoNewPrivileges" "$(systemctl show "$unit" -p NoNewPrivileges --value)" yes
  expect "$runner: unit PrivateTmp" "$(systemctl show "$unit" -p PrivateTmp --value)" yes
  expect "$runner: unit active" "$(systemctl show "$unit" -p ActiveState --value)" active
  uid="$(deadline 30 runtime_uid "$account" >/dev/null && runtime_uid "$account")"
  expect "$runner: runner process uid is the account's" "$uid" "$account_uid"
  check "$runner: runner authenticated to the controller with the credential in the account home" deadline 60 runner_seen "$runner"
  sudo cp "$unit_file" "$EVIDENCE/$unit"
}

note "1. shared runner under $SHARED_ACCOUNT (the documented setup)"
enroll "$SHARED_RUNNER" "$SHARED_ACCOUNT" --repos "$REPOSITORY"
expect "$SHARED_RUNNER: runners add --service-user exits 0" "$?" 0
account_checks "$SHARED_RUNNER" "$SHARED_ACCOUNT"
check "$SHARED_RUNNER: local admission is off" sudo grep -q 'local_admission: false' "$HOME_ROOT/$SHARED_ACCOUNT/.config/sparkwing/config.yaml"

note "1a. probes with the installed unit's own identity and sandbox lines"
# shellcheck disable=SC2016 # expanded by the probe's own shell
PROBE='for target in "$@"; do test -r "$target"; echo "readable_$(basename "$target")=$?"; done
ls /home/alice >/dev/null 2>&1; echo "list_alice_home=$?"
ls -A /home >/dev/null 2>&1; echo "list_home=$?"
echo "entries_in_home=$(ls -A /home 2>/dev/null | wc -l | tr -d " ")"
test -e /tmp/alice-shared-tmp-marker; echo "sees_host_tmp=$?"
echo "probe_uid=$(id -u)"'
TARGETS=(/home/alice/.ssh/id_ed25519 /home/alice/.aws/credentials /home/alice/notes.txt)
value() { sed -n "s/^$1=//p" "$2" | head -n1; }
# refused NAME VALUE: passes only for a recorded nonzero exit status, never for a missing line.
refused() { if [[ "$2" =~ ^[1-9][0-9]*$ ]]; then pass "$1 (exit $2)"; else fail "$1" "got '$2', want a nonzero exit status"; fi; }
# home_hidden NAME FILE: /home must be unlistable, or listable and empty; a missing measurement fails.
home_hidden() {
  local list entries
  list="$(value list_home "$2")"
  entries="$(value entries_in_home "$2")"
  if [[ "$list" =~ ^[1-9][0-9]*$ ]]; then
    pass "$1: /home cannot be listed (exit $list)"
  elif [[ "$list" == 0 && "$entries" == 0 ]]; then
    pass "$1: /home lists empty"
  else
    fail "$1: /home is hidden" "list exit '$list', entries '$entries'"
  fi
}
# The unit names no Group, so systemd uses the account's primary group, whatever its name.
mapfile -t PROPS < <(sudo grep -E '^(User|Group|ProtectHome|NoNewPrivileges|PrivateTmp|ProtectSystem|UMask)=' "$(unit_file_of "$SHARED_ACCOUNT")")
expect "unit sandbox lines found" "${#PROPS[@]}" 6
expect "unit names no Group" "$(printf '%s\n' "${PROPS[@]}" | grep -c '^Group=')" 0
printf '%s\n' "${PROPS[@]}" >"$EVIDENCE/unit-sandbox-lines.txt"
sudo -u alice -- /bin/sh -c "$PROBE" sh "${TARGETS[@]}" >"$EVIDENCE/probe-alice.txt"
expect "control: probe as alice exits 0" "$?" 0
for f in id_ed25519 credentials notes.txt; do expect "control: alice reads her $f" "$(value "readable_$f" "$EVIDENCE/probe-alice.txt")" 0; done
expect "control: alice lists her home" "$(value list_alice_home "$EVIDENCE/probe-alice.txt")" 0
expect "control: alice sees the host /tmp marker" "$(value sees_host_tmp "$EVIDENCE/probe-alice.txt")" 0
ARGS=()
for prop in "${PROPS[@]}"; do ARGS+=(-p "$prop"); done
sudo systemd-run --quiet --wait --pipe --collect "${ARGS[@]}" /bin/sh -c "$PROBE" sh "${TARGETS[@]}" >"$EVIDENCE/probe-unit.txt"
expect "unit probe exits 0" "$?" 0
for f in id_ed25519 credentials notes.txt; do refused "unit probe cannot read alice's $f" "$(value "readable_$f" "$EVIDENCE/probe-unit.txt")"; done
refused "unit probe cannot list alice's home" "$(value list_alice_home "$EVIDENCE/probe-unit.txt")"
home_hidden "unit probe" "$EVIDENCE/probe-unit.txt"
refused "unit probe cannot see the host /tmp marker" "$(value sees_host_tmp "$EVIDENCE/probe-unit.txt")"
expect "unit probe runs as the account" "$(value probe_uid "$EVIDENCE/probe-unit.txt")" "$(id -u "$SHARED_ACCOUNT")"
ARGS=()
for prop in "${PROPS[@]}"; do case "$prop" in ProtectHome=*) ;; *) ARGS+=(-p "$prop") ;; esac; done
sudo systemd-run --quiet --wait --pipe --collect "${ARGS[@]}" /bin/sh -c "$PROBE" sh "${TARGETS[@]}" >"$EVIDENCE/probe-uid-only.txt"
expect "uid-only probe exits 0" "$?" 0
refused "uid alone keeps out the 0600 ssh key" "$(value readable_id_ed25519 "$EVIDENCE/probe-uid-only.txt")"
refused "uid alone keeps out the 0600 aws credentials" "$(value readable_credentials "$EVIDENCE/probe-uid-only.txt")"
expect "uid alone still reads a world-readable home file, which ProtectHome hides" "$(value readable_notes.txt "$EVIDENCE/probe-uid-only.txt")" 0

note "2. personal runner under $JOB_ACCOUNT, which claims a real job"
# Shared --repos runners take only repository-proven work (GitHub App or Actions intake), which a sealed VM cannot produce;
# a personal runner takes its owner's asserted runs, and --service-user installs it through the same privileged path.
enroll "$JOB_RUNNER" "$JOB_ACCOUNT" --personal
expect "$JOB_RUNNER: runners add --personal --service-user exits 0" "$?" 0
account_checks "$JOB_RUNNER" "$JOB_ACCOUNT"

FIXTURE="$WORK/fixture"
mkdir -p "$FIXTURE/.sparkwing/jobs" "$FIXTURE/sdk"
cp "$REPO/testdata/runner-isolation/.sparkwing/main.go" "$FIXTURE/.sparkwing/"
cp "$REPO/testdata/runner-isolation/.sparkwing/jobs/isolation.go" "$FIXTURE/.sparkwing/jobs/"
cp "$REPO/testdata/k8s-e2e/repo/.sparkwing/go.sum" "$FIXTURE/.sparkwing/"
sed -e 's|^module .*|module sparkwing-runner-isolation|' -e 's|=> ../../../..$|=> ../sdk|' -e 's|=> ../../../../sparkwing$|=> ../sdk/sparkwing|' \
  "$REPO/testdata/k8s-e2e/repo/.sparkwing/go.mod" >"$FIXTURE/.sparkwing/go.mod"
setup sh -c "git -C '$REPO' archive HEAD -- go.mod go.sum build sparkwing sparkwingtest internal pkg | tar -x -C '$FIXTURE/sdk'"
setup git -C "$FIXTURE" init --quiet --initial-branch=main
setup git -C "$FIXTURE" add .
setup git -C "$FIXTURE" -c user.name='Isolation proof' -c user.email=proof@sparkwing.invalid -c commit.gpgsign=false commit --quiet --message 'test: runner isolation fixture'
setup git -C "$FIXTURE" bundle create "$WORK/source.bundle" --all
FIXTURE_SHA="$(git -C "$FIXTURE" rev-parse HEAD)"
BUNDLE_DIGEST="$(openssl dgst -sha256 "$WORK/source.bundle" | awk '{print $NF}')"
BUNDLE_SIZE="$(wc -c <"$WORK/source.bundle" | tr -d ' ')"
SOURCE="$(jq -nc --arg sha "$FIXTURE_SHA" --arg repo "$REPOSITORY" '{
  git:{repository:{key:("git:"+$repo),display:$repo},ref:"main",sha:$sha},
  source:{ref:$sha,entrypoint:"runner-isolation",location:{remote_url:("https://"+$repo+".git"),pipeline_dir:".sparkwing"}}}')"

# submit LABEL: uploads the bundle and starts a run; prints the run id.
submit() {
  local ticket upload_id url header body
  local -a headers=()
  ticket="$(api -X POST -H 'Content-Type: application/json' \
    --data "$(jq -c --arg d "$BUNDLE_DIGEST" --argjson s "$BUNDLE_SIZE" '. + {digest:$d,size:$s}' <<<"$SOURCE")" "$CONTROLLER/api/v1/data/source")" || return 1
  upload_id="$(jq -er .upload_id <<<"$ticket")" || return 1
  url="$(jq -er .url <<<"$ticket")" || return 1
  url="$CONTROLLER/${url#*://*/}"
  while IFS= read -r header; do headers+=(-H "$header"); done < <(jq -r '.headers // {} | to_entries[] | .key+": "+.value' <<<"$ticket")
  curl --fail --silent --show-error --max-time 60 -X PUT "${headers[@]}" --data-binary "@$WORK/source.bundle" "$url" >/dev/null || return 1
  api -X POST -H 'Content-Type: application/json' --data '{}' "$CONTROLLER/api/v1/data/source/$upload_id/commit" >/dev/null || return 1
  body="$(jq -nc --arg nonce "$1-$(openssl rand -hex 8)" --arg d "$BUNDLE_DIGEST" --arg team "$TEAM" --argjson src "$SOURCE" \
    '{idempotency_key:("isolation-proof/"+$nonce),request:($src + {version:1,team:$team,pipeline:"runner-isolation",mode:"execute",origin:"trigger",dispatch:"host"} | .source.bundle_digest={set:true,value:$d})}')"
  api -X POST -H 'Content-Type: application/json' --data "$body" "$CONTROLLER/api/v1/runs" | jq -er .run_id
}
run_status() { api "$CONTROLLER/api/v1/runs/$1" | jq -r .status; }
run_finished() { case "$(run_status "$1")" in success | failed | cancelled | done) return 0 ;; esac; return 1; }

RUN1="$(submit first)"
setup test -n "$RUN1"
deadline 900 run_finished "$RUN1"
expect "job run succeeded" "$(run_status "$RUN1")" success
api "$CONTROLLER/api/v1/runs/$RUN1" >"$EVIDENCE/run-1.json"
check "job attempt was executed by $JOB_RUNNER" jq -e --arg name "$JOB_RUNNER" '.attempts | any(.job_id == "probe-isolation" and .outcome == "success" and .executor_name == $name)' "$EVIDENCE/run-1.json"
api "$CONTROLLER/api/v1/runs/$RUN1/jobs/probe-isolation/output" | jq .output >"$EVIDENCE/job-output-1.json"
OUT="$EVIDENCE/job-output-1.json"
JOB_UID="$(id -u "$JOB_ACCOUNT")"
expect "job: uid is the service account's" "$(jq -r .uid "$OUT")" "$JOB_UID"
expect "job: effective uid is the service account's" "$(jq -r .euid "$OUT")" "$JOB_UID"
expect "job: user name" "$(jq -r .user "$OUT")" "$JOB_ACCOUNT"
expect "job: HOME is the account home" "$(jq -r .home "$OUT")" "$HOME_ROOT/$JOB_ACCOUNT"
expect "job: NoNewPrivs is set" "$(jq -r .no_new_privs "$OUT")" 1
outcome_of() { jq -r --arg t "$1" '.read[$t] // "missing"' "$OUT"; }
# hidden NAME OUTCOME: "denied" or "absent" is a measured denial or mount hiding; "allowed", an error or a missing probe fails.
hidden() { case "$2" in denied | absent) pass "$1 ($2)" ;; *) fail "$1" "outcome '$2'" ;; esac; }
expect "job: every target was probed" "$(jq -r '.read | keys | length' "$OUT")" 6
for target in /home/alice/.ssh/id_ed25519 /home/alice/.aws/credentials /home/alice/notes.txt /home/runner; do
  hidden "job: cannot open $target" "$(outcome_of "$target")"
done
expect "job: cannot list /root (a non-root uid is refused there without ProtectHome too, so this is not attributed to the sandbox)" "$(outcome_of /root)" denied
expect "job: the host /tmp marker is absent from its /tmp (PrivateTmp; alice's control saw it)" "$(outcome_of /tmp/alice-shared-tmp-marker)" absent
case "$(jq -r '.home_list // "missing"' "$OUT")" in
  denied) pass "job: /home cannot be listed (denied)" ;;
  empty) pass "job: /home lists empty" ;;
  *) fail "job: /home is hidden" "home_list '$(jq -r '.home_list // "missing"' "$OUT")'" ;;
esac
expect "job: wrote its own /tmp marker" "$(jq -r '.tmp_marker // "missing"' "$OUT")" allowed
check "job: its /tmp marker is not in the host /tmp (PrivateTmp)" sudo test ! -e /tmp/sparkwing-isolation-job-marker
if jq -e '.sudo_ran == true and .sudo_launch_err == "" and (.sudo_exit | type) == "number" and .sudo_exit != 0' "$OUT" >/dev/null; then
  pass "job: sudo -n true ran and was refused (exit $(jq -r .sudo_exit "$OUT"))"
else
  fail "job: sudo -n true ran and was refused" "$(jq -c '{sudo_ran, sudo_exit, sudo_launch_err}' "$OUT")"
fi
if jq -e '.sudo_stderr | ascii_downcase | contains("no new privileges")' "$OUT" >/dev/null; then
  pass "job: sudo named the no-new-privileges flag as the reason"
else
  info "job: sudo's refusal did not name the no-new-privileges flag: $(jq -c .sudo_stderr "$OUT")"
fi

note "3. pause and resume"
as_account "$JOB_ACCOUNT" sparkwing runners pause -o json >"$EVIDENCE/pause.json" 2>"$EVIDENCE/pause.err"
expect "pause as the account exits 0" "$?" 0
check "pause names $JOB_RUNNER and reports paused and changed" jq -e --arg r "$JOB_RUNNER" '.runner == $r and .paused == true and .changed == true' "$EVIDENCE/pause.json"
RUN2="$(submit paused)"
setup test -n "$RUN2"
SAMPLES="$EVIDENCE/pause-samples.tsv"
printf 'elapsed\tstatus\tattempts\tjobs\tlast_seen\n' >"$SAMPLES"
UNHELD=0
SEEN_FIRST=""
SEEN_PREV=""
SEEN_BAD=0
for _ in $(seq 1 12); do
  sleep 5
  row="$(api "$CONTROLLER/api/v1/runs/$RUN2" | jq -r '[.status, ((.attempts // []) | length), ((.jobs // []) | length)] | @tsv')" || row=""
  seen="$(runner_last_seen "$JOB_RUNNER")"
  printf '%s\t%s\t%s\n' "$SECONDS" "${row:-unreadable}" "$seen" >>"$SAMPLES"
  [[ "$row" == "pending"$'\t'"0"$'\t'"0" ]] || UNHELD=$((UNHELD + 1))
  ms="$(date -d "$seen" +%s%3N 2>/dev/null)" || ms=""
  if [[ -z "$seen" || "$seen" == null || -z "$ms" ]] || { [[ -n "$SEEN_PREV" ]] && ((ms < SEEN_PREV)); }; then
    SEEN_BAD=$((SEEN_BAD + 1))
  else
    SEEN_FIRST="${SEEN_FIRST:-$ms}"
    SEEN_PREV=$ms
  fi
done
expect "paused runner neither planned nor attempted the new run in 12 samples, 5s apart" "$UNHELD" 0
if ((SEEN_BAD == 0)) && [[ -n "$SEEN_FIRST" ]] && ((SEEN_PREV > SEEN_FIRST)); then
  pass "paused runner kept polling: last_seen valid in every sample and advanced $(((SEEN_PREV - SEEN_FIRST) / 1000))s"
else
  fail "paused runner kept polling" "$SEEN_BAD unreadable or decreasing last_seen samples; see pause-samples.tsv"
fi
as_account "$JOB_ACCOUNT" sparkwing runners resume -o json >"$EVIDENCE/resume.json" 2>"$EVIDENCE/resume.err"
expect "resume as the account exits 0" "$?" 0
deadline 600 run_finished "$RUN2"
expect "resumed runner runs the waiting job" "$(run_status "$RUN2")" success
api "$CONTROLLER/api/v1/runs/$RUN2" >"$EVIDENCE/run-2.json"
check "resumed run has exactly one job attempt, executed by $JOB_RUNNER" jq -e --arg name "$JOB_RUNNER" '[.attempts[] | select(.job_id == "probe-isolation")] | length == 1 and .[0].outcome == "success" and .[0].executor_name == $name' "$EVIDENCE/run-2.json"
api "$CONTROLLER/api/v1/runs/$RUN2/jobs/probe-isolation/output" | jq .output >"$EVIDENCE/job-output-2.json"
expect "resumed job also ran as the service account" "$(jq -r .uid "$EVIDENCE/job-output-2.json")" "$JOB_UID"

note "4. runners delete, credential revocation and account deletion"
for pair in "$SHARED_RUNNER:$SHARED_ACCOUNT" "$JOB_RUNNER:$JOB_ACCOUNT"; do
  runner=${pair%%:*}
  account=${pair#*:}
  unit="$(unit_of "$account")"
  uid="$(id -u "$account")"
  sudo journalctl -u "$unit" --no-pager >"$EVIDENCE/journal-$account.txt" 2>&1
  mapfile -t credential_files < <(sudo find "$HOME_ROOT/$account" -name agent-token -type f)
  expect "$runner: exactly one runner credential file" "${#credential_files[@]}" 1
  credential="$(sudo cat "${credential_files[0]:-/nonexistent}" 2>/dev/null)"
  mask "$credential"
  check "$runner: runner credential is nonempty" test -n "$credential"
  # A machine credential is authenticated but lacks runs.read on this route, so 403 is the positive control.
  expect "$runner: its credential authenticates before deletion (403, authenticated without runs.read)" "$(api_status "$credential" /api/v1/runners)" 403
  as_alice sparkwing runners delete --profile work --service-user "$account" -o json >"$EVIDENCE/delete-$runner.json" 2>"$EVIDENCE/delete-$runner.err"
  expect "$runner: runners delete --service-user exits 0" "$?" 0
  check "$runner: delete reports the token revoked" jq -e '.token.revoked == true' "$EVIDENCE/delete-$runner.json"
  expect "$runner: its credential is unauthenticated after deletion" "$(api_status "$credential" /api/v1/runners)" 401
  expect "$runner: control: the team credential still authenticates after the deletion" "$(api_status "$TEAM_TOKEN" /api/v1/runners)" 200
  check "$runner: unit file removed" test ! -e "$(unit_file_of "$account")"
  expect "$runner: unit no longer loaded" "$(systemctl show "$unit" -p LoadState --value)" not-found
  check "$runner: no process runs as the account" sh -c "! pgrep -u $uid >/dev/null"
  setup sudo userdel --remove "$account"
  check "$runner: account deleted" sh -c "! getent passwd $account >/dev/null"
  check "$runner: account group deleted" sh -c "! getent group $account >/dev/null"
  check "$runner: account home deleted" sudo test ! -e "$HOME_ROOT/$account"
  if sudo test -e "$HOME_ROOT/.enrollments/$account.json"; then
    info "$runner: root-only enrollment record remains at $HOME_ROOT/.enrollments/$account.json after delete and userdel"
  else
    info "$runner: enrollment record removed"
  fi
done

note "5. nothing left running or installed"
expect "no sparkwing runner unit files remain" "$(systemctl list-unit-files --no-legend 'sparkwing-runner-*' | wc -l | tr -d ' ')" 0
expect "no sparkwing runner units loaded" "$(systemctl list-units --all --no-legend 'sparkwing-runner-*' | wc -l | tr -d ' ')" 0
expect "no staging directories remain" "$(sudo find "$HOME_ROOT" -maxdepth 1 -name '.stage-*' | wc -l | tr -d ' ')" 0
check "no sparkwing-runner process remains" sh -c '! pgrep -x sparkwing-runner >/dev/null'
sudo ls -la "$HOME_ROOT" "$HOME_ROOT/.enrollments" >"$EVIDENCE/final-var-lib-sparkwing.txt" 2>&1

stop_controller || true
CONTROLLER_PID=""
if ((FAILURES == 0)); then
  record "RESULT: PASS"
  exit 0
fi
record "RESULT: FAIL ($FAILURES failed checks)"
exit 1
