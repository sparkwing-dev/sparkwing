package runners

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

func TestNativeInstallationPreservesRegistrationAndConfiguration(t *testing.T) {
	release := runnerReleaseFixture(t)
	dir := t.TempDir()
	executable := filepath.Join(dir, "sparkwing-runner")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	for name, body := range map[string]string{filepath.Base(executable): "previous executable", "runner.yaml": "existing configuration", "credential": "existing credential", "schedule": "existing schedule"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var calls []string
	config := NativeInstallationConfig{
		Executable: executable,
		Drain:      func(context.Context) error { calls = append(calls, "drain"); return nil },
		RefreshManagedUnits: func(_ context.Context, version string) error {
			calls = append(calls, "refresh")
			if version != release.Version() {
				t.Fatalf("refresh version = %q", version)
			}
			assertInstalledRunner(t, executable, release.Asset().Bytes())
			return nil
		},
		Restart: func(context.Context) error { calls = append(calls, "restart"); return nil },
	}
	installation, err := NewNativeInstallation(config)
	if err != nil {
		t.Fatal(err)
	}
	update := engine.SoftwareUpdate{CurrentVersion: "v0.10.0", Version: release.Version(), Release: release, Required: true}
	if err := applySoftwareUpdate(t.Context(), update, installation); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"drain", "refresh", "restart"}) {
		t.Fatalf("calls = %v", calls)
	}
	for name, want := range map[string]string{"runner.yaml": "existing configuration", "credential": "existing credential", "schedule": "existing schedule"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(body) != want {
			t.Fatalf("%s = %q, %v", name, body, err)
		}
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("installed mode = %o", info.Mode().Perm())
	}
}

func TestNativeInstallationStopsAtFailure(t *testing.T) {
	release := runnerReleaseFixture(t)
	failure := errors.New("registration unavailable")
	for _, step := range []string{"drain", "refresh", "restart", "unverified", "cancelled", "directory"} {
		t.Run(step, func(t *testing.T) {
			executable := filepath.Join(t.TempDir(), "runner")
			if err := os.WriteFile(executable, []byte("previous"), 0o700); err != nil {
				t.Fatal(err)
			}
			var calls []string
			hook := func(name string) func(context.Context) error {
				return func(context.Context) error {
					calls = append(calls, name)
					if name == step {
						return failure
					}
					return nil
				}
			}
			config := NativeInstallationConfig{Executable: executable, Drain: hook("drain"), RefreshManagedUnits: func(ctx context.Context, _ string) error { return hook("refresh")(ctx) }, Restart: hook("restart")}
			if step == "directory" {
				config.Executable = filepath.Dir(executable)
			}
			installation, err := NewNativeInstallation(config)
			if err != nil {
				t.Fatal(err)
			}
			update := engine.SoftwareUpdate{CurrentVersion: "v0.10.0", Version: release.Version(), Release: release, Required: true}
			if step == "unverified" {
				update.Release = engine.VerifiedRelease{}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if step == "cancelled" {
				cancel()
			}
			err = applySoftwareUpdate(ctx, update, installation)
			if err == nil {
				t.Fatal("installation succeeded")
			}
			want := []string{"drain"}
			switch step {
			case "unverified", "cancelled":
				want = nil
			case "refresh":
				want = append(want, "refresh")
			case "restart":
				want = append(want, "refresh", "restart")
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %v, want %v", calls, want)
			}
			if step == "refresh" || step == "restart" {
				assertInstalledRunner(t, executable, release.Asset().Bytes())
			} else {
				assertInstalledRunner(t, executable, []byte("previous"))
			}
		})
	}
}

func TestNativeInstallationRequiresLifecycleHooksAndAbsolutePath(t *testing.T) {
	hook := func(context.Context) error { return nil }
	refresh := func(context.Context, string) error { return nil }
	for _, config := range []NativeInstallationConfig{
		{},
		{Executable: "runner", Drain: hook, RefreshManagedUnits: refresh, Restart: hook},
		{Executable: filepath.Join(t.TempDir(), "runner"), RefreshManagedUnits: refresh, Restart: hook},
		{Executable: filepath.Join(t.TempDir(), "runner"), Drain: hook, Restart: hook},
		{Executable: filepath.Join(t.TempDir(), "runner"), Drain: hook, RefreshManagedUnits: refresh},
	} {
		if _, err := NewNativeInstallation(config); err == nil {
			t.Fatal("incomplete installation accepted")
		}
	}
}

func TestNativeInstallationRetriesUnitRefreshWithoutReplacingAgain(t *testing.T) {
	release := runnerReleaseFixture(t)
	path := filepath.Join(t.TempDir(), "runner")
	if err := os.WriteFile(path, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	refreshErr := errors.New("service manager unavailable")
	restarts := 0
	installation, err := NewNativeInstallation(NativeInstallationConfig{
		Executable:          path,
		Drain:               func(context.Context) error { return nil },
		RefreshManagedUnits: func(context.Context, string) error { return refreshErr },
		Restart:             func(context.Context) error { restarts++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	update := engine.SoftwareUpdate{Version: release.Version(), Release: release}
	if err := applySoftwareUpdate(t.Context(), update, installation); !errors.Is(err, refreshErr) {
		t.Fatalf("first update = %v", err)
	}
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	refreshErr = nil
	if err := applySoftwareUpdate(t.Context(), update, installation); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(first, second) || restarts != 1 {
		t.Fatalf("replacement repeated = %v, restarts = %d", !os.SameFile(first, second), restarts)
	}
}

func TestNativeInstallationPreservesExecutableSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation requires machine privileges")
	}
	release := runnerReleaseFixture(t)
	dir := t.TempDir()
	path, link := filepath.Join(dir, "runner"), filepath.Join(dir, "installed-runner")
	if err := os.WriteFile(path, []byte("previous"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	hook := func(context.Context) error { return nil }
	installation, err := NewNativeInstallation(NativeInstallationConfig{Executable: link, Drain: hook, RefreshManagedUnits: func(context.Context, string) error { return nil }, Restart: hook})
	if err != nil {
		t.Fatal(err)
	}
	if err := applySoftwareUpdate(t.Context(), engine.SoftwareUpdate{Version: release.Version(), Release: release}, installation); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(link); err != nil || got != path {
		t.Fatalf("symlink = %q, %v", got, err)
	}
	assertInstalledRunner(t, link, release.Asset().Bytes())
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("staging files remain: %v, %v", entries, err)
	}
}

func TestRunningRunnerReplacementPreservesRecoverableImage(t *testing.T) {
	failure := errors.New("native move failed")
	for _, failingMove := range []int{0, 1, 2, 3} {
		t.Run(string(rune('0'+failingMove)), func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, "new.exe"), filepath.Join(dir, "runner.exe")
			for path, body := range map[string]string{source: "new", target: "old", target + ".old": "stale"} {
				if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			moves := 0
			move := func(from, to string) error {
				moves++
				if moves == failingMove || failingMove == 3 && moves == 2 {
					return failure
				}
				return os.Rename(from, to)
			}
			err := replaceRunningRunner(source, target, move, os.Remove)
			if (err != nil) != (failingMove != 0) {
				t.Fatalf("replace = %v", err)
			}
			switch failingMove {
			case 0:
				assertInstalledRunner(t, target, []byte("new"))
				assertInstalledRunner(t, target+".old", []byte("old"))
			case 1, 2:
				assertInstalledRunner(t, target, []byte("old"))
			case 3:
				assertInstalledRunner(t, target+".old", []byte("old"))
			}
		})
	}
}

func assertInstalledRunner(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("installed bytes match = %v, error = %v", bytes.Equal(got, want), err)
	}
}
