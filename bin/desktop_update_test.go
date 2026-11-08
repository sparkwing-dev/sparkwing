package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestDesktopUpdateWaitsForWorkAndRefusesUnverifiedIdle(t *testing.T) {
	for _, mode := range []string{"idle", "drain", "run", "trigger", "lease", "land", "opaque-land", "status-error", "malformed", "missing-count", "units-error", "malformed-units", "show-error", "red", "build-error", "restart-error", "stopped"} {
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
[ "$1 $2" = 'daemon status' ]
[ "$SPARKWING_HOME" = "$HOME/state" ] || [ "$SPARKWING_HOME" = "$HOME/delivery" ]
echo status >> "$HOME/calls"
case "$MODE" in
status-error) exit 1 ;;
malformed) echo invalid; exit ;;
missing-count) echo '{"kind":"service","tool":"sparkwing","service":"controller","state":"running","activity":{}}'; exit ;;
esac
runs=0; triggers=0; leases=0; state=running
case "$MODE" in
run) runs=1 ;;
drain) [ -e "$HOME/drained" ] || runs=1 ;;
trigger) triggers=1 ;;
lease) leases=1 ;;
stopped) state=stopped ;;
esac
printf '{"kind":"service","tool":"sparkwing","service":"controller","state":"%s","activity":{"runs":%s,"queued_triggers":%s,"held_leases":%s}}\n' "$state" "$runs" "$triggers" "$leases"
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
			} else if string(cli) != string(before) || string(runner) != "old runner\n" || strings.Contains(string(calls), "restart") {
				t.Fatalf("refused update changed installation: %s (%s)", calls, out)
			}
			if !success && !strings.Contains(string(out), "desktop-update:") {
				t.Fatalf("failure lacks updater diagnostic: %s", out)
			}
		})
	}
}
