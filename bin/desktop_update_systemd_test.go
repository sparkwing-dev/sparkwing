//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestDesktopUpdaterSystemdLifetimeAndControllerKeys(t *testing.T) {
	cli := os.Getenv("SPARKWING_LOCAL_E2E_BINARY")
	if cli == "" {
		t.Skip("set SPARKWING_LOCAL_E2E_BINARY to a worktree candidate")
	}
	if err := procenv.ControlCommandContext(t.Context(), os.Environ(), "systemctl", "--user", "show-environment").Run(); err != nil {
		t.Skip("systemd user manager unavailable")
	}
	for _, mode := range []string{"keys", "default-config", "unit-exit", "cancel-build", "cancel-restart"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			dir := t.TempDir()
			home, tools, dest := filepath.Join(dir, "home"), filepath.Join(dir, "tools"), filepath.Join(dir, "install")
			for _, p := range []string{home, tools, dest} {
				if err := os.MkdirAll(p, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			config := filepath.Join(home, "config.yaml")
			if mode == "default-config" {
				config = filepath.Join(home, "config", "sparkwing", "config.yaml")
				if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write(config, "admission: {budget: ignore-external}\ndashboard: {address: '127.0.0.1:0'}\n")
			if err := os.Chmod(config, 0o600); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(cli)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dest, "sparkwing"), data, 0o700); err != nil {
				t.Fatal(err)
			}
			if mode == "cancel-restart" {
				if err := os.WriteFile(filepath.Join(home, "candidate-cli"), data, 0o700); err != nil {
					t.Fatal(err)
				}
				write(filepath.Join(dest, "sparkwing"), `#!/bin/bash
set -eu
if [ "$1 $2" = 'daemon restart' ]; then
 /bin/sleep 300 &
 echo "$!" > "$HOME/build-pid"
 wait
fi
exec "$HOME/candidate-cli" "$@"
`)
			}
			write(filepath.Join(dest, "sparkwing-runner"), "#!/bin/sh\nexit 0\n")
			write(filepath.Join(tools, "git"), `#!/bin/bash
set -eu
case "$3" in
 remote) echo fixture-origin;;
 rev-parse) echo fixture-main;;
 clone) target="${@: -1}"; mkdir -p "$target/bin"; cp "$HOME/installer" "$target/bin/install.sh";;
 *) exit 90;;
esac
`)
			write(filepath.Join(tools, "gh"), `#!/bin/sh
echo '[{"headSha":"fixture-main","status":"completed","conclusion":"success"}]'
`)
			write(filepath.Join(tools, "systemctl"), "#!/bin/sh\n[ \"$2\" = list-units ] || exit 90\necho '[]'\n")
			write(filepath.Join(home, "installer"), `#!/bin/bash
set -eu
if [ -e "$HOME/cancel-build" ]; then
 /bin/sleep 300 &
 echo "$!" > "$HOME/build-pid"
 wait
fi
mkdir -p "$SPARKWING_INSTALL_BIN"
cp "$HOME/../install/sparkwing" "$SPARKWING_INSTALL_BIN/sparkwing"
cp "$HOME/../install/sparkwing-runner" "$SPARKWING_INSTALL_BIN/sparkwing-runner"
`)
			base := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "SPARKWING_CONFIG="+filepath.Join(home, "config.yaml"), "SPARKWING_SECRETS_KEY=", "SPARKWING_SECRETS_PREVIOUS_KEY=", "SPARKWING_SECRETS_KEY_FILE=", "INVOCATION_ID=")
			if mode == "default-config" {
				base = slices.DeleteFunc(base, func(entry string) bool { return strings.HasPrefix(entry, "SPARKWING_CONFIG=") })
			}
			run := func(environment []string, name string, args ...string) []byte {
				t.Helper()
				out, err := procenv.ControlCommandContext(ctx, environment, name, args...).CombinedOutput()
				if err != nil {
					t.Fatalf("%s %v: %v: %s", name, args, err, out)
				}
				return out
			}
			unit := fmt.Sprintf("sparkwing-updater-test-%d-%s", os.Getpid(), mode)
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
				defer stop()
				_ = procenv.ControlCommandContext(cleanup, os.Environ(), "systemctl", "--user", "stop", unit+".service").Run()
			})
			var homes []string
			var pids []int
			if mode != "cancel-build" {
				for _, state := range []string{filepath.Join(home, ".sparkwing"), filepath.Join(home, "delivery")} {
					homes = append(homes, state)
					environment := append(append([]string{}, base...), "SPARKWING_HOME="+state)
					if mode == "keys" && len(homes) == 2 {
						environment = append(environment, "SPARKWING_SECRETS_KEY_FILE="+filepath.Join(home, "custom delivery key"))
					}
					t.Cleanup(func() {
						cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
						defer stop()
						_ = procenv.ControlCommandContext(cleanup, environment, cli, "daemon", "stop", "-o", "json").Run()
					})
					cmd := procenv.ControlCommandContext(ctx, environment, cli, "secrets", "set", "--secret", "UPDATER_TEST", "-o", "json")
					cmd.Stdin = strings.NewReader("fixture encrypted value")
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("seed encrypted store: %v: %s", err, out)
					}
					status, err := wingdclient.QueryLocalController(ctx, state)
					if err != nil || !status.Running {
						t.Fatalf("initial controller: %v", err)
					}
					pids = append(pids, status.PID)
				}
			} else {
				write(filepath.Join(home, "cancel-build"), "")
				homes = []string{filepath.Join(home, ".sparkwing")}
			}
			workerEnv := append(append([]string{}, base...), "PATH="+tools+":"+os.Getenv("PATH"))
			args := append([]string{filepath.Join(".", "desktop-update.sh"), dir, dest, "0"}, homes...)
			script, err := filepath.Abs(args[0])
			if err != nil {
				t.Fatal(err)
			}
			args[0] = script
			if mode == "keys" || mode == "default-config" {
				run(workerEnv, "bash", args...)
			} else {
				launch := []string{"--user", "--quiet", "--collect", "--unit=" + unit, "--property=Type=exec", "--property=KillMode=control-group"}
				for _, name := range []string{"HOME", "PATH", "XDG_CONFIG_HOME", "SPARKWING_CONFIG", "SPARKWING_SECRETS_KEY_FILE"} {
					value := ""
					for _, entry := range workerEnv {
						if strings.HasPrefix(entry, name+"=") {
							value = strings.TrimPrefix(entry, name+"=")
						}
					}
					launch = append(launch, "--setenv="+name+"="+value)
				}
				if mode == "unit-exit" {
					launch = append(launch, "--wait", "--pipe")
				}
				launch = append(launch, "--", "bash")
				launch = append(launch, args...)
				run(base, "systemd-run", launch...)
				if strings.HasPrefix(mode, "cancel-") {
					var pid int
					for pid == 0 {
						if b, err := os.ReadFile(filepath.Join(home, "build-pid")); err == nil {
							pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
						}
						if pid != 0 {
							break
						}
						select {
						case <-ctx.Done():
							t.Fatal("updater child did not start")
						// sleepcheck:external-boundary await the fixture build child in the real user unit
						case <-time.After(20 * time.Millisecond):
						}
					}
					group, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
					if err != nil || !strings.Contains(string(group), unit) {
						t.Fatalf("updater child escaped its cgroup: %s, %v", group, err)
					}
					run(base, "systemctl", "--user", "stop", unit+".service")
					if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !os.IsNotExist(err) {
						t.Fatal("updater child outlived its unit")
					}
					return
				}
			}
			for i, state := range homes {
				status, err := wingdclient.QueryLocalController(ctx, state)
				if err != nil || !status.Running || status.PID == pids[i] {
					t.Fatalf("replacement did not survive updater exit: %v", err)
				}
				group, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", status.PID))
				if err != nil || strings.Contains(string(group), unit) {
					t.Fatalf("replacement retained updater cgroup: %s, %v", group, err)
				}
				environment := append(append([]string{}, base...), "SPARKWING_HOME="+state)
				rotated := run(environment, cli, "secrets", "rotate", "-o", "json")
				var rotation struct {
					Rotated int               `json:"rotated"`
					Skipped []json.RawMessage `json:"skipped"`
				}
				if err := json.Unmarshal(rotated, &rotation); err != nil || rotation.Rotated != 1 || len(rotation.Skipped) != 0 {
					t.Fatalf("stored secret could not be opened and resealed: %v", err)
				}
				out := run(environment, cli, "secrets", "list", "-o", "json")
				if !strings.Contains(string(out), "UPDATER_TEST") {
					t.Fatal("stored secret was lost after updater restart")
				}
			}
		})
	}
}
