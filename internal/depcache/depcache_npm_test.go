package depcache

import (
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/testhome"
)

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
	got, err := resolveNpmCacheDir(t.TempDir())
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
	got, err := resolveNpmCacheDir(t.TempDir())
	if err != nil || got != want {
		t.Fatalf("uppercase cache = %q %v, want %q", got, err, want)
	}
}

func TestResolveNpmCacheUsesProjectNpmrc(t *testing.T) {
	if _, err := pipelineentry.NPMCommand(t.Context(), goruntime.GOOS, exec.LookPath, "config", "get", "cache"); err != nil {
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
	probe, probeErr := pipelineentry.NPMCommand(t.Context(), goruntime.GOOS, exec.LookPath, "config", "get", "cache")
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	probe.Dir = root
	if output, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("npm query %q %#v: %v: %s", probe.Path, probe.Args, err, output)
	}
	got, err := resolveNpmCacheDir(root)
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
	got, err := resolveNpmCacheDir(t.TempDir())
	if err != nil || strings.TrimSpace(got) != want {
		t.Fatalf("fallback cache = %q %v, want %q", got, err, want)
	}
}
