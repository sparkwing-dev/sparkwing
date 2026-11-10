//go:build linux

package wingd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestManagedUnitsPrivateConfigWithSystemd(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_SYSTEMD_UNITS") != "1" {
		t.Skip("set SPARKWING_TEST_SYSTEMD_UNITS=1 for private real-manager units")
	}
	root := t.TempDir()
	execute := func(ctx context.Context, name string, args ...string) (string, error) {
		out, err := procenv.ControlCommandContext(ctx, os.Environ(), "/usr/bin/"+name, args...).CombinedOutput()
		t.Logf("%s %s: %s", name, strings.Join(args, " "), out)
		return string(out), err
	}
	host := crontimer.Host{GOOS: "linux", Home: root, ConfigHome: filepath.Join(root, "private-config"), SparkwingHome: filepath.Join(root, "state"), Binary: "/bin/true", Version: "v0.0.0-test", Exec: execute}
	manager, err := NewManagedUnitManager(host)
	if err != nil {
		t.Fatal(err)
	}
	native := manager.(*nativeUnits)
	name := strings.TrimSuffix(filepath.Base(native.path()), ".service")
	if name == "sparkwing-controller" {
		t.Fatal("private controller must have a unique name")
	}
	t.Cleanup(func() {
		files, err := filepath.Glob(filepath.Join(crontimer.UnitDir(host), "*"))
		if err != nil {
			t.Error(err)
		}
		if err := manager.UninstallController(context.Background()); err != nil {
			t.Error(err)
		}
		if _, err := crontimer.Uninstall(context.Background(), host); err != nil {
			t.Error(err)
		}
		for _, file := range files {
			out, _ := execute(context.Background(), "systemctl", "--user", "show", "-p", "FragmentPath", "--value", filepath.Base(file))
			if strings.TrimSpace(out) != "" {
				t.Errorf("registration remains for %s: %s", file, out)
			}
		}
		for _, pattern := range []string{native.path(), filepath.Join(crontimer.UnitDir(host), "*.timer"), filepath.Join(crontimer.UnitDir(host), "*.service")} {
			files, err := filepath.Glob(pattern)
			if err != nil || len(files) != 0 {
				t.Errorf("cleanup left %s: %v, %v", pattern, files, err)
			}
		}
	})
	for range 2 {
		state, err := crontimer.Install(t.Context(), host)
		if err != nil {
			t.Fatalf("install timer: %+v, %v", state, err)
		}
		state, err = crontimer.Status(t.Context(), host)
		if err != nil || !state.Enabled {
			t.Fatalf("timer status: %+v, %v", state, err)
		}
		if err := manager.InstallController(t.Context(), LocalStartOptions{Home: host.SparkwingHome, Executable: host.Binary, Version: host.Version}); err != nil {
			t.Fatal(err)
		}
		if err := manager.Refresh(t.Context(), host.Binary, host.Version); err != nil {
			t.Fatal(err)
		}
	}
}
