package repos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultPath_NeverResolvesToTheRealRegistryUnderTest is the
// always-on half of BW-1457's acceptance criterion. The full-suite audit
// in internal/configguard proves the suite left the live registry alone
// on one machine at one moment; this proves no fixture can reach it at
// all, which is the property that has to survive the next fixture
// someone writes.
func TestDefaultPath_NeverResolvesToTheRealRegistryUnderTest(t *testing.T) {
	t.Setenv("SPARKWING_REPOS", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	live := filepath.Join(home, ".config", "sparkwing", "repos.yaml")
	if got == live {
		t.Fatalf("DefaultPath resolved to the developer's own registry %s", live)
	}
	if !strings.HasPrefix(got, os.TempDir()) {
		t.Errorf("DefaultPath = %q, want a path under %s", got, os.TempDir())
	}
}

// TestDefaultPath_StillHonorsAnExplicitOverride keeps the sandbox from
// swallowing the redirection tests already rely on.
func TestDefaultPath_StillHonorsAnExplicitOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "explicit.yaml")
	t.Setenv("SPARKWING_REPOS", want)
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if got != want {
		t.Errorf("DefaultPath = %q, want the SPARKWING_REPOS value %q", got, want)
	}

	t.Setenv("SPARKWING_REPOS", "")
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	got, err = DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(xdg, "sparkwing", "repos.yaml"); got != want {
		t.Errorf("DefaultPath = %q, want %q", got, want)
	}
}

// TestAutoRegister_SkipsAScratchCheckoutUnderTempDir covers the entry
// class that grew the registry to 457: a repo scaffolded under
// os.TempDir(), run once, and deleted.
func TestAutoRegister_SkipsAScratchCheckoutUnderTempDir(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "repos.yaml")
	t.Setenv("SPARKWING_REPOS", registry)

	scratch, err := os.MkdirTemp("", "sparkwing-tv-fake-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	if err := os.MkdirAll(filepath.Join(scratch, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(scratch, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := AutoRegister(scratch); err != nil {
		t.Fatalf("AutoRegister: %v", err)
	}
	cfg, err := Load(registry)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Repos) != 0 {
		t.Errorf("a scratch checkout under %s was registered: %+v", os.TempDir(), cfg.Repos)
	}
}

// TestUnderTempDir is the control for the temp skip: the class it
// filters is throwaway directories, not every checkout. It asserts on
// underTempDir rather than on AutoRegister because t.TempDir() is itself
// inside os.TempDir() on macOS and Linux, which leaves a test no way to
// build a "real" checkout on disk to register.
func TestUnderTempDir(t *testing.T) {
	scratch, err := os.MkdirTemp("", "sparkwing-tv-fake-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })

	for _, tc := range []struct {
		path string
		want bool
	}{
		{scratch, true},
		{filepath.Join(scratch, "nested", "checkout"), true},
		{os.TempDir(), true},
		{filepath.Join(string(filepath.Separator), "Users", "dev", "code", "app"), false},
		{filepath.Join(string(filepath.Separator), "home", "dev", "code", "app"), false},
		{filepath.Join(string(filepath.Separator), "opt", "src", "app"), false},
	} {
		if got := underTempDir(tc.path); got != tc.want {
			t.Errorf("underTempDir(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestAutoRegister_StillRecordsACheckoutOutsideTempDir keeps the skip
// from swallowing ordinary registration. The checkout does not exist on
// disk, so this drives Add's shared validation only as far as the temp
// filter, which is the branch under test.
func TestAutoRegister_StillRecordsACheckoutOutsideTempDir(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "repos.yaml")
	t.Setenv("SPARKWING_REPOS", registry)

	outside := filepath.Join(string(filepath.Separator), "Users", "dev", "code", "app")
	err := AutoRegister(outside)
	if err == nil {
		t.Fatal("AutoRegister accepted a path with no .git, so it never reached repoKind")
	}
	if !strings.Contains(err.Error(), outside) {
		t.Errorf("AutoRegister(%q) failed with %v, want an error naming the path: the temp filter swallowed it", outside, err)
	}
}
