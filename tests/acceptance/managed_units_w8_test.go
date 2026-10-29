package acceptance_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestManagedUnitsW8Components(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			root := t.TempDir()
			host := crontimer.Host{
				GOOS: goos, Home: root, ConfigHome: filepath.Join(root, "config"),
				SparkwingHome: filepath.Join(root, ".sparkwing"), Binary: filepath.Join(root, "bin with $ and space", "sparkwing"),
				Version: "v1.2.3-foo--bar", PathEnv: "/captured/tools", UID: 501,
				Exec: func(_ context.Context, name string, args ...string) (string, error) {
					if name == "launchctl" && args[0] == "print" {
						return "path = " + filepath.Join(root, "Library", "LaunchAgents", filepath.Base(args[1])+".plist") + "\n", nil
					}
					return "", nil
				},
			}
			manager, err := wingd.NewManagedUnitManager(host)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := crontimer.Install(t.Context(), host); err != nil {
				t.Fatal(err)
			}
			if err := manager.InstallController(t.Context(), wingd.LocalStartOptions{Home: host.SparkwingHome, Executable: host.Binary, Version: host.Version}); err != nil {
				t.Fatal(err)
			}
			if err := manager.Refresh(t.Context(), host.Binary, "v1.2.4"); err != nil {
				t.Fatal(err)
			}
			units, err := manager.Inspect(t.Context())
			if err != nil || len(units) != 2 {
				t.Fatalf("units = %+v, %v", units, err)
			}
			for _, unit := range units {
				want := []string{"__tick", "--home", host.SparkwingHome}
				if unit.Kind == wingd.ManagedController {
					want = []string{"wingd", "run", "--stay", "--home", host.SparkwingHome}
				}
				if unit.Home != host.SparkwingHome || unit.Executable != host.Binary || unit.Version != "v1.2.4" || !slices.Equal(unit.Arguments, want) {
					t.Fatalf("unit = %+v", unit)
				}
				body, err := os.ReadFile(unit.Name)
				if err != nil || strings.Contains(string(body), "SPARKWING_HOME") || strings.Contains(string(body), "crons tick") {
					t.Fatalf("unit restored removed plumbing: %s, %v", body, err)
				}
			}
			if _, err := os.Stat(filepath.Join(host.SparkwingHome, "state.db")); !os.IsNotExist(err) {
				t.Fatalf("unit management opened a store: %v", err)
			}
		})
	}
}
