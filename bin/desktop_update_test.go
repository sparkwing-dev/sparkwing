package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestDesktopUpdateWaitsForWorkAndRefusesUnverifiedIdle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd updater")
	}
	for _, mode := range []string{"idle", "drain", "run", "trigger", "lease", "land", "opaque-land", "status-error", "malformed", "missing-count", "missing-pid", "environment-error", "units-error", "malformed-units", "show-error", "red", "build-error", "rename-error", "rollback-error", "restart-error", "stopped"} {
		t.Run(mode, func(t *testing.T) {
			root, err := filepath.Abs(".")
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			home := filepath.Join(dir, "home")
			bin := filepath.Join(dir, "tools")
			dest := filepath.Join(home, ".local", "bin")
			for _, path := range []string{home, bin, dest} {
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(bin, "git"), `#!/bin/bash
set -eu
case "$3" in
remote) echo https://github.com/sparkwing-dev/sparkwing ;;
rev-parse) echo selected ;;
clone)
  target="${@: -1}"
  mkdir -p "$target/bin"
  cp "$FIXTURE_INSTALLER" "$target/bin/install.sh"
  ;;
*) exit 1 ;;
esac
`)
			write(filepath.Join(dir, "install.sh"), `#!/bin/bash
set -eu
[ "$SPARKWING_INSTALL_BIN" != "$HOME/.local/bin" ]
echo build >> "$HOME/calls"
[ "$MODE" != build-error ] || exit 1
mkdir -p "$SPARKWING_INSTALL_BIN"
cp "$FIXTURE_CANDIDATE" "$SPARKWING_INSTALL_BIN/sparkwing"
printf 'new runner\n' > "$SPARKWING_INSTALL_BIN/sparkwing-runner"
chmod +x "$SPARKWING_INSTALL_BIN/sparkwing-runner"
`)
			write(filepath.Join(dir, "candidate"), `#!/bin/bash
set -eu
[ "$1 $2 $3" = 'daemon restart --force' ]
[ "$SPARKWING_HOME" = "$HOME/state" ] || [ "$SPARKWING_HOME" = "$HOME/delivery" ]
echo restart >> "$HOME/calls"
[ "$MODE" != restart-error ]
`)
			write(filepath.Join(bin, "gh"), `#!/bin/bash
set -eu
result=success
[ "$MODE" != red ] || result=failure
printf '[{"headSha":"selected","status":"completed","conclusion":"%s"}]\n' "$result"
`)
			write(filepath.Join(dest, "sparkwing"), `#!/bin/bash
set -eu
if [ "$1 $2" = 'runs list' ]; then
  if [ -n "${UPDATER_REAL_CLI:-}" ]; then SPARKWING_SECRETS_KEY_FILE="$SPARKWING_HOME/key" exec "$UPDATER_REAL_CLI" "$@"; fi
  case "$*" in
    'runs list --profile= --status pending --since 0 --limit 5 -o json') ids='4 3 2 1'; status=pending ;;
    'runs list --profile= --status running --since 0 --limit 5 -o json') ids='7 6 5'; status=running ;;
    *) exit 1 ;;
  esac
  for n in $ids; do
    printf '{"id":"run-blocker-%s","pipeline":"check","status":"%s"}\n' "$n" "$status"
  done
  echo '{"kind":"page","truncated":false}'
  exit
fi
[ "$1 $2" = 'daemon status' ]
[ "$SPARKWING_HOME" = "$HOME/state" ] || [ "$SPARKWING_HOME" = "$HOME/delivery" ]
echo status >> "$HOME/calls"
case "$MODE" in
status-error) exit 1 ;;
malformed) echo invalid; exit ;;
missing-count) echo '{"kind":"service","tool":"sparkwing","service":"controller","state":"running","activity":{}}'; exit ;;
missing-pid) FIXTURE_CONTROLLER_PID=0 ;;
environment-error) FIXTURE_CONTROLLER_PID=2147483647 ;;
esac
runs=0; triggers=0; leases=0; state=running
case "$MODE" in
run) runs=7 ;;
drain) [ -e "$HOME/drained" ] || runs=1 ;;
trigger) triggers=1 ;;
lease) leases=1 ;;
stopped) state=stopped ;;
esac
printf '{"kind":"service","tool":"sparkwing","service":"controller","state":"%s","pid":%s,"activity":{"runs":%s,"queued_triggers":%s,"held_leases":%s}}\n' "$state" "$FIXTURE_CONTROLLER_PID" "$runs" "$triggers" "$leases"
`)
			write(filepath.Join(bin, "systemctl"), `#!/bin/bash
set -eu
case "$2" in
list-units)
  echo units >> "$HOME/calls"
  [ "$MODE" != units-error ] || exit 1
  case "$MODE" in
    land) echo '[{"unit":"sw-land-42.service"}]' ;;
    opaque-land|show-error) echo '[{"unit":"worker.service"}]' ;;
    malformed-units) echo '{}' ;;
    drain) if [ -e "$HOME/drained" ]; then echo '[]'; else echo '[{"unit":"sw-land-42.service"}]'; fi ;;
    *) echo '[]' ;;
  esac
  ;;
show)
  [ "$MODE" != show-error ] || exit 1
  echo '{ path=/private/.local/bin/sparkwing ; argv[]=/private/.local/bin/sparkwing run land ; }'
  ;;
*) exit 1 ;;
esac
`)
			write(filepath.Join(bin, "sleep"), "#!/bin/sh\ntouch \"$HOME/drained\"\n")
			write(filepath.Join(bin, "go"), "#!/bin/sh\nexit 1\n")
			write(filepath.Join(bin, "pnpm"), "#!/bin/sh\nexit 1\n")
			write(filepath.Join(bin, "mv"), `#!/bin/bash
set -eu
if [[ ( "$MODE" == rename-error || "$MODE" == rollback-error ) && "${@: -1}" == "$HOME/.local/bin/sparkwing" ]]; then
  echo 'injected CLI rename failure' >&2
  exit 30
fi
if [[ "$MODE" == rollback-error && "$*" == *previous-runner* ]]; then exit 31; fi
exec /bin/mv "$@"
`)
			write(filepath.Join(dest, "sparkwing-runner"), "old runner\n")
			before, err := os.ReadFile(filepath.Join(dest, "sparkwing"))
			if err != nil {
				t.Fatal(err)
			}
			wait := "0"
			if mode == "drain" {
				wait = "60"
			}
			environment := append(os.Environ(), "HOME="+home, "SPARKWING_HOME="+filepath.Join(home, "state"), "MODE="+mode,
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "FIXTURE_INSTALLER="+filepath.Join(dir, "install.sh"), "FIXTURE_CANDIDATE="+filepath.Join(dir, "candidate"))
			if cli := os.Getenv("SPARKWING_LOCAL_E2E_BINARY"); cli != "" && mode == "run" {
				for _, state := range []string{filepath.Join(home, "state"), filepath.Join(home, "delivery")} {
					if err := os.MkdirAll(state, 0o700); err != nil {
						t.Fatal(err)
					}
					db, err := teststore.Open(t, filepath.Join(state, "state.db"))
					if err != nil {
						t.Fatal(err)
					}
					for n := 1; n <= 8; n++ {
						status := "pending"
						if n > 4 {
							status = "running"
						}
						if n == 8 {
							status = "failed"
						}
						if err := teststore.SeedRun(db, t.Context(), store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "run-blocker-" + strconv.Itoa(n), Pipeline: "check", Status: status, DeclaredRepo: "acme/widgets"}}); err != nil {
							t.Fatal(err)
						}
					}
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
					realEnv := append(environment, "SPARKWING_SECRETS_KEY_FILE="+filepath.Join(state, "key"), "SPARKWING_HOME="+state, "SPARKWING_CONFIG="+filepath.Join(home, "config.yaml"), "XDG_CONFIG_HOME="+filepath.Join(home, "config"))
					if out, err := procenv.ControlCommandContext(t.Context(), realEnv, cli, "daemon", "restart", "--force", "-o", "json").CombinedOutput(); err != nil {
						t.Fatalf("start real controller: %v: %s", err, out)
					}
					t.Cleanup(func() {
						ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
						defer cancel()
						if out, err := procenv.ControlCommandContext(ctx, realEnv, cli, "daemon", "stop", "-o", "json").CombinedOutput(); err != nil {
							t.Errorf("stop real controller: %v: %s", err, out)
						}
					})
				}
				environment = append(environment, "UPDATER_REAL_CLI="+cli, "SPARKWING_CONFIG="+filepath.Join(home, "config.yaml"), "XDG_CONFIG_HOME="+filepath.Join(home, "config"))
			}
			controller := procenv.ControlCommand(environment, "/bin/sleep", "300")
			if err := controller.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = controller.Process.Kill(); _ = controller.Wait() })
			environment = append(environment, "FIXTURE_CONTROLLER_PID="+strconv.Itoa(controller.Process.Pid))
			cmd := procenv.ControlCommandContext(t.Context(), environment, "bash", filepath.Join(root, "desktop-update.sh"), dir, dest, wait, filepath.Join(home, "state"), filepath.Join(home, "delivery"))
			out, err := cmd.CombinedOutput()
			success := mode == "idle" || mode == "drain" || mode == "stopped"
			if (err == nil) != success {
				t.Fatalf("success=%v error=%v: %s", success, err, out)
			}
			cli, readErr := os.ReadFile(filepath.Join(dest, "sparkwing"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			runner, err := os.ReadFile(filepath.Join(dest, "sparkwing-runner"))
			if err != nil {
				t.Fatal(err)
			}
			calls, _ := os.ReadFile(filepath.Join(home, "calls"))
			if success || mode == "restart-error" {
				if string(cli) == string(before) || string(runner) != "new runner\n" {
					t.Fatalf("binaries did not swap: %s", out)
				}
				want := 2
				if mode == "stopped" {
					want = 0
				} else if mode == "restart-error" {
					want = 1
				}
				if strings.Count(string(calls), "restart\n") != want {
					t.Fatalf("restart calls: %s (%s)", calls, out)
				}
				if mode == "drain" && !strings.Contains(string(out), "waiting for sw-land-42.service") {
					t.Fatalf("land unit was not awaited: %s", out)
				}
			} else if mode == "rollback-error" {
				if string(cli) != string(before) || string(runner) != "new runner\n" || strings.Contains(string(calls), "restart") || !strings.Contains(string(out), "mixed versions installed; restore") || !strings.Contains(string(out), "previous-runner to "+dest+"/sparkwing-runner before retrying") {
					t.Fatalf("failed rollback lacks repair guidance: %s (%s)", calls, out)
				}
			} else if string(cli) != string(before) || string(runner) != "old runner\n" || strings.Contains(string(calls), "restart") {
				t.Fatalf("refused update changed installation: %s (%s)", calls, out)
			}
			if !success && !strings.Contains(string(out), "desktop-update:") {
				t.Fatalf("failure lacks updater diagnostic: %s", out)
			}
			if mode == "run" {
				for _, want := range []string{"blocking run run-blocker-1 (check, pending)", "blocking run run-blocker-7 (check, running)", "7 active runs", "showing up to 5", `runs list --profile= --status "$status" --since 0 -o json`, "drain timed out"} {
					if !strings.Contains(string(out), want) {
						t.Fatalf("missing blocking-run diagnostic %q: %s", want, out)
					}
				}
				if os.Getenv("SPARKWING_LOCAL_E2E_BINARY") != "" {
					for _, line := range strings.Split(string(out), "\n") {
						if command, ok := strings.CutPrefix(line, "desktop-update: inspect: "); ok {
							details, err := procenv.ControlCommandContext(t.Context(), environment, "bash", "-c", command).CombinedOutput()
							if err != nil || !strings.Contains(string(details), "run-blocker-6") || strings.Contains(string(details), "run-blocker-8") {
								t.Fatalf("inspection command did not list all active runs: %v: %s", err, details)
							}
						}
					}
				}
				if strings.Contains(string(out), "blocking run run-blocker-6") {
					t.Fatalf("blocking-run diagnostic exceeded cap: %s", out)
				}
			}
			if mode == "rename-error" && !strings.Contains(string(out), "previous binaries restored") {
				t.Fatalf("CLI rename failure lacks recovery diagnostic: %s", out)
			}
		})
	}
}
