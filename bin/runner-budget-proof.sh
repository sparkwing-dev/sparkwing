#!/usr/bin/env bash
set -euo pipefail

REPO=$(realpath "$(dirname "$0")/..")
if [[ ${1:-} == --preflight ]]; then
  exec bash "$REPO/bin/runner-isolation-proof.sh" --preflight "${2:-}"
fi
# Reuse the privileged proof's host facts before creating any state.
bash "$REPO/bin/runner-isolation-proof.sh" --preflight || exit 2
[[ $# == 1 && -n $1 ]] || { echo "usage: $0 EVIDENCE_DIR" >&2; exit 2; }
EVIDENCE=$(realpath -m "$1")
WORK="${RUNNER_TEMP:?}/runner-budget-work"
mkdir -p "$EVIDENCE" "$WORK/bin"
exec > >(tee "$EVIDENCE/proof.log") 2>&1
export GOWORK=off
XDG_RUNTIME_DIR="/run/user/$(id -u)"
export XDG_RUNTIME_DIR
export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"

finish() {
  local code=$?
  trap - EXIT
  if [[ -n ${SPARKWING_HOME:-} ]]; then
    timeout 45 "$WORK/bin/sparkwing" daemon stop >"$EVIDENCE/cleanup.log" 2>&1 || code=1
  fi
  if ((code != 0)); then echo 'RESULT: FAIL (see proof.log)'; fi
  if [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
    { echo '### Runner CPU budget proof'; cat "$EVIDENCE/verdict.txt" 2>/dev/null || echo 'Proof did not complete.'; } >>"$GITHUB_STEP_SUMMARY"
  fi
  exit "$code"
}
trap finish EXIT

{
  git -C "$REPO" rev-parse HEAD
  uname -a
  systemctl --version
  go version
  nproc
  cat /proc/self/cgroup
  lscpu
} >"$EVIDENCE/environment.txt"
sudo -n loginctl enable-linger "$(id -un)"
sudo -n systemctl start "user@$(id -u).service"
systemctl --user show-environment >"$WORK/user-manager-environment.txt"
loginctl show-user "$(id -un)" -p Linger -p State >"$EVIDENCE/user-manager.txt"
VERSION="v0.0.0-dev+$(git -C "$REPO" rev-parse --short=12 HEAD)"
go -C "$REPO" build -trimpath -ldflags "-X main.Version=$VERSION" -o "$WORK/bin/sparkwing" ./cmd/sparkwing
sha256sum "$WORK/bin/sparkwing" >"$EVIDENCE/binary.sha256"

for case_name in enforce control; do
  CASE="$WORK/$case_name"
  mkdir -p "$CASE/home" "$CASE/state" "$CASE/repo/.sparkwing"
  export HOME="$CASE/home" SPARKWING_HOME="$CASE/state" SPARKWING_CONFIG="$CASE/config.yaml"
  export SPARKWING_SECRETS_KEY_FILE="$CASE/secrets.key"
  export XDG_CONFIG_HOME="$CASE/home/.config" XDG_CACHE_HOME="$CASE/home/.cache"
  # setup-go's toolchain must remain usable after HOME changes.
  export GOCACHE="$WORK/go-cache" GOPATH="$WORK/go-path"
  printf 'dashboard: {address: "127.0.0.1:0"}\n' >"$SPARKWING_CONFIG"
  if [[ $case_name == enforce ]]; then printf 'admission: {budget: "2,enforce"}\n' >>"$SPARKWING_CONFIG"; fi
  chmod 0600 "$SPARKWING_CONFIG"
  [[ $(stat -c %a "$SPARKWING_CONFIG") == 600 ]]
  cp "$SPARKWING_CONFIG" "$EVIDENCE/$case_name-config.yaml"
  stat -c '%a %n' "$SPARKWING_CONFIG" >"$EVIDENCE/$case_name-config-mode.txt"
  sed 's|module github.com/sparkwing-dev/sparkwing/sparkwing|module budgetproof|' "$REPO/sparkwing/go.mod" >"$CASE/repo/.sparkwing/go.mod"
  printf '\nrequire github.com/sparkwing-dev/sparkwing/sparkwing v0.0.0\nreplace github.com/sparkwing-dev/sparkwing/sparkwing => "%s/sparkwing"\n' "$REPO" >>"$CASE/repo/.sparkwing/go.mod"
  cp "$REPO/sparkwing/go.sum" "$CASE/repo/.sparkwing/go.sum"
  printf 'pipelines:\n  - name: proof\n    entrypoint: proof\n' >"$CASE/repo/.sparkwing/sparkwing.yaml"
  cat >"$CASE/repo/.sparkwing/main.go" <<EOF
package main
import("context";"os";"os/exec";sw "github.com/sparkwing-dev/sparkwing/sparkwing")
type pipeline struct{sw.Base}
func(pipeline) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{
 sw.Job(p,"burn",func(ctx context.Context)error{
  c:=exec.CommandContext(ctx,"python3","-I","$REPO/bin/runner-budget-probe.py","measure","$EVIDENCE/$case_name.json")
  c.Stdout=os.Stdout;c.Stderr=os.Stderr;return c.Run()
 }).Cores(2);return nil
}
func main(){sw.Register("proof",func()sw.Pipeline[sw.NoInputs]{return pipeline{}});sw.Main()}
EOF
  git -C "$CASE/repo" init -q
  git -C "$CASE/repo" add .sparkwing
  git -C "$CASE/repo" -c user.name=Proof -c user.email=proof@example.invalid -c commit.gpgsign=false commit -qm fixture
  cp -a "$CASE/repo" "$EVIDENCE/$case_name-fixture"
  timeout 300 "$WORK/bin/sparkwing" run proof -C "$CASE/repo" --route local --no-update -o json >"$EVIDENCE/$case_name-run.jsonl" 2>&1
  UNIT=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["unit"])' "$EVIDENCE/$case_name.json")
  [[ $UNIT == sparkwing-runner-*.service ]]
  systemctl --user show "$UNIT" -p Id -p MainPID -p ControlGroup -p CPUQuotaPerSecUSec -p CPUQuotaPeriodUSec -p Nice >"$EVIDENCE/$case_name-unit.txt"
  "$WORK/bin/sparkwing" queue list -o json >"$EVIDENCE/$case_name-queue.jsonl"
  timeout 45 "$WORK/bin/sparkwing" daemon stop >"$EVIDENCE/$case_name-stop.txt" 2>&1
  unset SPARKWING_HOME
done
python3 -I "$REPO/bin/runner-budget-probe.py" judge "$EVIDENCE/enforce.json" "$EVIDENCE/control.json" 2>&1 | tee "$EVIDENCE/verdict.txt"
