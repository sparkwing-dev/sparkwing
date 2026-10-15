package acceptance_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/cluster"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runners/service"
)

func TestRunnerServiceW8Components(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			root := t.TempDir()
			var calls []string
			host := service.Host{
				GOOS: goos, Home: root, ConfigHome: filepath.Join(root, "config"),
				Binary:     filepath.Join(root, "bin with spaces", "sparkwing-runner"),
				ConfigPath: filepath.Join(root, "config", "runner.yaml"), UID: 501,
				Exec: func(_ context.Context, name string, args ...string) (string, error) {
					calls = append(calls, name+" "+strings.Join(args, " "))
					if name == "launchctl" && args[0] == "print" {
						return "path = " + filepath.Join(root, "Library", "LaunchAgents", service.PlistName) + "\nstate = running\n", nil
					}
					return "", nil
				},
			}
			credentials, err := fssecure.MkdirPrivateTemp(root, "credentials-")
			if err != nil {
				t.Fatal(err)
			}
			if err := fssecure.WriteFile(filepath.Join(credentials, "agent-token"), []byte("private-runner-credential")); err != nil {
				t.Fatal(err)
			}
			config := []byte("runner:\n  controller: https://controller.example.com\n  credentials_dir: " + credentials + "\n")
			if err := os.MkdirAll(filepath.Dir(host.ConfigPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(host.ConfigPath, config, 0o600); err != nil {
				t.Fatal(err)
			}
			if state, err := service.Refresh(t.Context(), host); err != nil || state.Installed || len(calls) != 0 {
				t.Fatalf("refresh installed an absent registration: %+v %v calls=%v", state, err, calls)
			}
			if _, err := service.Install(t.Context(), host); err != nil {
				t.Fatal(err)
			}
			state, err := service.Inspect(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if !state.Installed || state.Binary != host.Binary || !slices.Equal(state.Arguments, []string{"start", "--config", host.ConfigPath}) {
				t.Fatalf("installed command=%+v", state)
			}
			reachedRuntime := errors.New("reached the unified runner")
			cli := cluster.RunnerCLI{
				Factory: func(ctx context.Context, options cluster.RunnerOptions) (runners.RuntimeConfig, error) {
					if options.Bearer == nil {
						t.Fatal("installed command did not load its private credential")
					}
					if bearer, err := options.Bearer(ctx); err != nil || bearer != "private-runner-credential" {
						t.Fatalf("installed command lost its credential: %v", err)
					}
					return runners.RuntimeConfig{}, reachedRuntime
				},
			}
			if err := cli.Run(t.Context(), state.Arguments[0], state.Arguments[1:], "v0.20.0"); !errors.Is(err, reachedRuntime) {
				t.Fatalf("installed argv did not reach the final runner: %v", err)
			}
			calls = nil
			host.Binary = filepath.Join(root, "updated bin", "sparkwing-runner")
			if state, err := service.Refresh(t.Context(), host); err != nil || !state.Installed {
				t.Fatalf("refresh=%+v %v", state, err)
			}
			state, err = service.Inspect(t.Context(), host)
			if err != nil || state.Binary != host.Binary || !slices.Equal(state.Arguments, []string{"start", "--config", host.ConfigPath}) {
				t.Fatalf("refreshed command=%+v err=%v", state, err)
			}
			if goos == "linux" && !slices.Equal(calls, []string{"systemctl --user show -p FragmentPath --value sparkwing-runner.service", "systemctl --user daemon-reload", "systemctl --user try-restart sparkwing-runner.service"}) {
				t.Fatalf("refresh changed registration: %v", calls)
			}
			if goos == "darwin" && !slices.Equal(calls, []string{"launchctl print gui/501/com.sparkwing.runner", "launchctl bootout gui/501/com.sparkwing.runner", "launchctl bootstrap gui/501 " + state.Path}) {
				t.Fatalf("refresh changed registration: %v", calls)
			}
			if body, err := os.ReadFile(host.ConfigPath); err != nil || !slices.Equal(body, config) {
				t.Fatalf("config changed: %s %v", body, err)
			}
			if _, err := service.Uninstall(t.Context(), host); err != nil {
				t.Fatal(err)
			}
			if state, err := service.Inspect(t.Context(), host); err != nil || state.Installed {
				t.Fatalf("uninstall=%+v %v", state, err)
			}
		})
	}
}
