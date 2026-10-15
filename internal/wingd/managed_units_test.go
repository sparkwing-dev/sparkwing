package wingd

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
)

func TestManagedUnitsInstallRefreshAndRemove(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			var calls []string
			root := t.TempDir()
			host := crontimer.Host{GOOS: goos, Home: root, ConfigHome: filepath.Join(root, "config"),
				SparkwingHome: filepath.Join(root, ".sparkwing"), Binary: filepath.Join(root, "bin with spaces", "sparkwing"), Version: "v1.2.3-foo--bar", UID: 501,
				PathEnv: "/captured/tools:/usr/bin", Exec: func(_ context.Context, name string, args ...string) (string, error) {
					calls = append(calls, name+" "+strings.Join(args, " "))
					if name == "launchctl" && args[0] == "print" {
						return "path = " + filepath.Join(root, "Library", "LaunchAgents", filepath.Base(args[1])+".plist") + "\n", nil
					}
					return "", nil
				}}
			manager, err := NewManagedUnitManager(host)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Refresh(t.Context(), host.Binary, "v1.2.3"); err != nil || len(calls) != 0 {
				t.Fatalf("refresh installed missing units: %v %v", err, calls)
			}
			if _, err := crontimer.Install(t.Context(), host); err != nil {
				t.Fatal(err)
			}
			if err := manager.InstallController(t.Context(), LocalStartOptions{Home: host.SparkwingHome, Executable: host.Binary, Version: host.Version}); err != nil {
				t.Fatal(err)
			}
			units, err := manager.Inspect(t.Context())
			if err != nil || len(units) != 2 {
				t.Fatalf("installed units = %+v, %v", units, err)
			}
			for _, unit := range units {
				if unit.Home != host.SparkwingHome || unit.Executable != host.Binary || unit.Version != host.Version || !validUnitArguments(unit) {
					t.Fatalf("installed unit = %+v", unit)
				}
				body, err := os.ReadFile(unit.Name)
				if err != nil {
					t.Fatal(err)
				}
				if unit.Kind == ManagedController {
					if goos == "darwin" {
						if err := xml.Unmarshal(body, new(struct{})); err != nil {
							t.Fatal(err)
						}
					}
					if goos == "linux" && (!strings.Contains(string(body), "Restart=on-failure") || !strings.Contains(string(body), "WantedBy=default.target")) {
						t.Fatalf("controller does not survive login/crash:\n%s", body)
					}
					if goos == "darwin" && (!strings.Contains(string(body), "<key>RunAtLoad</key><true/>") || !strings.Contains(string(body), "<key>SuccessfulExit</key><false/>")) {
						t.Fatalf("controller does not survive login/crash:\n%s", body)
					}
					if strings.Contains(string(body), "socket") || strings.Contains(string(body), "dashboard start") {
						t.Fatalf("controller is not a foreground service:\n%s", body)
					}
				}
			}
			calls = nil
			next := filepath.Join(root, "next bin", "sparkwing")
			if err := manager.Refresh(t.Context(), next, "v1.2.4"); err != nil {
				t.Fatal(err)
			}
			units, err = manager.Inspect(t.Context())
			if err != nil || len(units) != 2 {
				t.Fatalf("refreshed units = %+v, %v", units, err)
			}
			for _, unit := range units {
				if unit.Executable != next || unit.Version != "v1.2.4" || unit.Home != host.SparkwingHome {
					t.Fatalf("refresh lost the unit: %+v", unit)
				}
			}
			want := "systemctl --user restart sparkwing-controller.service"
			if goos == "darwin" {
				want = "launchctl bootout gui/501/dev.sparkwing.controller"
			}
			if !slices.Contains(calls, want) {
				t.Fatalf("update did not restart the installed controller: %v", calls)
			}
			if err := manager.UninstallController(t.Context()); err != nil {
				t.Fatal(err)
			}
			units, err = manager.Inspect(t.Context())
			if err != nil || len(units) != 1 || units[0].Kind != ManagedSchedule {
				t.Fatalf("uninstall changed schedule registration: %+v, %v", units, err)
			}
		})
	}
}

func TestManagedControllerRefreshRetriesFailedBootstrap(t *testing.T) {
	root := t.TempDir()
	registered, fail := false, false
	want := errors.New("bootstrap failed")
	host := crontimer.Host{GOOS: "darwin", Home: root, SparkwingHome: filepath.Join(root, ".sparkwing"), UID: 501,
		Exec: func(_ context.Context, _ string, args ...string) (string, error) {
			switch args[0] {
			case "print":
				if !registered {
					return "Could not find service", errors.New("exit 113")
				}
				return "path = " + filepath.Join(root, "Library", "LaunchAgents", "dev.sparkwing.controller.plist") + "\n", nil
			case "bootout":
				registered = false
			case "bootstrap":
				if fail {
					fail = false
					return "", want
				}
				registered = true
			}
			return "", nil
		}}
	manager, err := NewManagedUnitManager(host)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "sparkwing")
	if err := manager.InstallController(t.Context(), LocalStartOptions{Home: host.SparkwingHome, Executable: executable, Version: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	fail = true
	if err := manager.Refresh(t.Context(), executable, "v1.2.4"); !errors.Is(err, want) || registered {
		t.Fatalf("failed reload = %v, registered=%v", err, registered)
	}
	if err := manager.Refresh(t.Context(), executable, "v1.2.4"); err != nil || !registered {
		t.Fatalf("retry left the controller stopped: %v, registered=%v", err, registered)
	}
}

func TestManagedUnitsRefuseWindowsAndOtherHomes(t *testing.T) {
	root := t.TempDir()
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			calls := 0
			host := crontimer.Host{GOOS: goos, Home: root, ConfigHome: filepath.Join(root, "config"), SparkwingHome: filepath.Join(root, "state"),
				Exec: func(context.Context, string, ...string) (string, error) { calls++; return "", nil }}
			manager, err := NewManagedUnitManager(host)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.InstallController(t.Context(), LocalStartOptions{Home: filepath.Join(root, "another-home"), Executable: filepath.Join(root, "sparkwing"), Version: "v1"}); err == nil || calls != 0 {
				t.Fatalf("changed another home: %v calls=%d", err, calls)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := manager.Refresh(ctx, filepath.Join(root, "sparkwing"), "v1"); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled refresh = %v", err)
			}
			if goos == "windows" {
				if err := manager.InstallController(t.Context(), LocalStartOptions{Home: host.SparkwingHome, Executable: filepath.Join(root, "sparkwing"), Version: "v1"}); err == nil || !strings.Contains(err.Error(), "--stay") {
					t.Fatalf("Windows install = %v", err)
				}
				if units, err := manager.Inspect(t.Context()); err != nil || len(units) != 0 {
					t.Fatalf("Windows native units = %+v, %v", units, err)
				}
			}
		})
	}
}

func TestManagedControllerInstallRefusesAnotherLoadedFile(t *testing.T) {
	root := t.TempDir()
	var calls []string
	host := crontimer.Host{GOOS: "linux", Home: root, ConfigHome: filepath.Join(root, "config"), SparkwingHome: filepath.Join(root, ".sparkwing"),
		Exec: func(_ context.Context, name string, args ...string) (string, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return "/another/config/sparkwing-controller.service\n", nil
		}}
	manager, err := NewManagedUnitManager(host)
	if err != nil {
		t.Fatal(err)
	}
	err = manager.InstallController(t.Context(), LocalStartOptions{Home: host.SparkwingHome, Executable: filepath.Join(root, "sparkwing"), Version: "v1.2.3"})
	if err == nil || len(calls) != 1 || !strings.Contains(calls[0], "FragmentPath") {
		t.Fatalf("installation changed another registration: %v %v", err, calls)
	}
	if _, err := os.Stat(filepath.Join(host.ConfigHome, "systemd", "user", "sparkwing-controller.service")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused install wrote its unit: %v", err)
	}
}
