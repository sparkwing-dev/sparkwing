package sparkwing

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testhome"
)

func TestNpmCacheCommandUsesWindowsNodeCLI(t *testing.T) {
	directory := filepath.Join("tool directory with spaces", "node")
	npm := filepath.Join(directory, "npm.cmd")
	node := filepath.Join(directory, "node.exe")
	lookPath := func(name string) (string, error) {
		if name == "npm" {
			return npm, nil
		}
		if name == "node" {
			return node, nil
		}
		return "", exec.ErrNotFound
	}
	command, err := npmCacheCommand(t.Context(), "windows", lookPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{node, filepath.Join(directory, "node_modules", "npm", "bin", "npm-cli.js"), "config", "get", "cache"}
	if command.Path != node || !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("command = %q %#v, want %#v", command.Path, command.Args, want)
	}
}

func TestNpmCacheCommandPreservesUnixNpmInvocation(t *testing.T) {
	npm := filepath.Join("npm directory with spaces", "npm")
	for _, goos := range []string{"linux", "darwin"} {
		command, err := npmCacheCommand(t.Context(), goos, func(name string) (string, error) {
			if name != "npm" {
				t.Fatalf("unexpected %s lookup", name)
			}
			return npm, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{npm, "config", "get", "cache"}; command.Path != npm || !reflect.DeepEqual(command.Args, want) {
			t.Fatalf("%s command = %#v, want %#v", goos, command.Args, want)
		}
	}
}

func TestNpmCacheCommandPropagatesMissingNode(t *testing.T) {
	command, err := npmCacheCommand(t.Context(), "windows", func(name string) (string, error) {
		if name == "npm" {
			return "npm.cmd", nil
		}
		return "", exec.ErrNotFound
	})
	if command != nil || !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing node = %v %v", command, err)
	}
}

func TestNpmCacheCommandAllowsNativeWindowsNpm(t *testing.T) {
	command, err := npmCacheCommand(t.Context(), "windows", func(name string) (string, error) {
		if name != "npm" {
			t.Fatalf("unexpected %s lookup", name)
		}
		return "npm.exe", nil
	})
	if err != nil || command.Path != "npm.exe" || !reflect.DeepEqual(command.Args, []string{"npm.exe", "config", "get", "cache"}) {
		t.Fatalf("native npm = %v %v", command, err)
	}
}

func TestDefaultNpmCacheUsesWindowsLocalAppData(t *testing.T) {
	local := filepath.Join(t.TempDir(), "Local AppData")
	t.Setenv("LOCALAPPDATA", local)
	got, err := defaultNpmCacheDir("windows")
	if want := filepath.Join(local, "npm-cache"); err != nil || got != want {
		t.Fatalf("cache = %q %v, want %q", got, err, want)
	}
}

func TestDefaultNpmCacheWithoutLocalAppData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOCALAPPDATA", "")
	testhome.Set(t, home)
	t.Setenv("USERPROFILE", home)
	for _, tc := range []struct{ goos, relative string }{{"windows", filepath.Join("AppData", "Local", "npm-cache")}, {"linux", ".npm"}, {"darwin", ".npm"}} {
		got, err := defaultNpmCacheDir(tc.goos)
		if want := filepath.Join(home, tc.relative); err != nil || got != want {
			t.Fatalf("%s cache = %q %v, want %q", tc.goos, got, err, want)
		}
	}
}

func TestResolveNpmCacheHonorsEnvironmentBeforeLookup(t *testing.T) {
	want := filepath.Join(t.TempDir(), "explicit cache")
	t.Setenv("npm_config_cache", want)
	t.Setenv("PATH", t.TempDir())
	got, err := resolveNpmCacheDir()
	if err != nil || got != want {
		t.Fatalf("explicit cache = %q %v, want %q", got, err, want)
	}
}

func TestResolveNpmCacheWindowsUppercaseEnvironment(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("Windows environment names are case insensitive")
	}
	want := filepath.Join(t.TempDir(), "explicit cache")
	t.Setenv("NPM_CONFIG_CACHE", want)
	t.Setenv("PATH", t.TempDir())
	got, err := resolveNpmCacheDir()
	if err != nil || got != want {
		t.Fatalf("uppercase cache = %q %v, want %q", got, err, want)
	}
}

func TestResolveNpmCacheUsesProjectNpmrc(t *testing.T) {
	if _, err := npmCacheCommand(context.Background(), goruntime.GOOS, exec.LookPath); err != nil {
		t.Skipf("npm not installed: %v", err)
	}
	t.Setenv("npm_config_cache", "")
	if err := os.Unsetenv("npm_config_cache"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	want := filepath.Join(root, "configured cache")
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"cache-fixture","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".npmrc"), []byte("cache="+filepath.ToSlash(want)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	probe, probeErr := npmCacheCommand(t.Context(), goruntime.GOOS, exec.LookPath)
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	probe.Dir = root
	if output, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("npm query %q %#v: %v: %s", probe.Path, probe.Args, err, output)
	}
	previous := WorkDir()
	SetWorkDir(root)
	t.Cleanup(func() { SetWorkDir(previous) })
	got, err := resolveNpmCacheDir()
	if err != nil || filepath.Clean(got) != want {
		t.Fatalf("project npmrc cache = %q %v, want %q", got, err, want)
	}
}

func TestResolveNpmCacheFallsBackWhenNpmUnavailable(t *testing.T) {
	t.Setenv("npm_config_cache", "")
	t.Setenv("PATH", t.TempDir())
	want, err := defaultNpmCacheDir(goruntime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveNpmCacheDir()
	if err != nil || strings.TrimSpace(got) != want {
		t.Fatalf("fallback cache = %q %v, want %q", got, err, want)
	}
}
