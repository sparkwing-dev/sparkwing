package bincache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

// TestSparkwingHome_UnderTestNeverResolvesToTheRealHome is the guarantee
// this package was missing. It resolved SPARKWING_HOME and ~/.sparkwing
// itself, bypassing the sandbox redirect in internal/paths, so a fixture
// that forgot to isolate compiled pipeline binaries into the developer's
// real cache -- and, once Prune landed, evicted from it.
func TestSparkwingHome_UnderTestNeverResolvesToTheRealHome(t *testing.T) {
	t.Setenv("SPARKWING_HOME", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	real := filepath.Join(home, ".sparkwing")

	for _, tc := range []struct {
		name string
		got  string
	}{
		{"SparkwingHome", SparkwingHome()},
		{"CacheRoot", CacheRoot()},
		{"CachedBinaryPath", CachedBinaryPath("deadbeef")},
	} {
		if tc.got == real || strings.HasPrefix(tc.got, real+string(filepath.Separator)) {
			t.Errorf("%s = %q, which is inside the developer's own home %s", tc.name, tc.got, real)
		}
		if !strings.HasPrefix(tc.got, os.TempDir()) {
			t.Errorf("%s = %q, want a path under the test sandbox %s", tc.name, tc.got, os.TempDir())
		}
	}
}

// TestSparkwingHome_HonorsSparkwingHomeEnv pins the precedence real runs
// depend on: an explicit SPARKWING_HOME wins, and the cache layout under
// it is unchanged.
func TestSparkwingHome_HonorsSparkwingHomeEnv(t *testing.T) {
	want := t.TempDir()
	t.Setenv("SPARKWING_HOME", want)

	if got := SparkwingHome(); got != want {
		t.Errorf("SparkwingHome() = %q, want the SPARKWING_HOME value %q", got, want)
	}
	if got, wantRoot := CacheRoot(), filepath.Join(want, "cache", "pipelines"); got != wantRoot {
		t.Errorf("CacheRoot() = %q, want %q", got, wantRoot)
	}
	wantBin := filepath.Join(want, "cache", "pipelines", "deadbeef", binaryName())
	if got := CachedBinaryPath("deadbeef"); got != wantBin {
		t.Errorf("CachedBinaryPath() = %q, want %q", got, wantBin)
	}
}

// TestSparkwingHome_MatchesDefaultPaths is the unification invariant. It
// is what keeps the ~/.sparkwing default honest without this test having
// to resolve the real home: internal/paths owns that answer and tests it
// there, and this asserts the cache agrees with it in every mode rather
// than carrying a second copy of the rule.
func TestSparkwingHome_MatchesDefaultPaths(t *testing.T) {
	for _, env := range []string{"", t.TempDir()} {
		t.Setenv("SPARKWING_HOME", env)
		p, err := paths.DefaultPaths()
		if err != nil {
			t.Fatalf("DefaultPaths: %v", err)
		}
		if got := SparkwingHome(); got != p.Root {
			t.Errorf("SPARKWING_HOME=%q: SparkwingHome() = %q, want the paths root %q", env, got, p.Root)
		}
	}
}

// TestPrune_UnisolatedRunsAgainstTheSandbox exercises the redirect the
// way the LRU prune reaches it. Prune reads CacheRoot, so a suite that
// forgot to isolate used to walk -- and evict from -- the real cache.
func TestPrune_UnisolatedRunsAgainstTheSandbox(t *testing.T) {
	t.Setenv("SPARKWING_HOME", "")

	root := CacheRoot()
	if !strings.HasPrefix(root, os.TempDir()) {
		t.Fatalf("CacheRoot() = %q, want a path under the test sandbox", root)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	seedEntry(t, "sandbox-cold", 100, 72*time.Hour)
	seedEntry(t, "sandbox-warm", 100, 48*time.Hour)

	result, err := Prune(150, 0)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Removed != 1 {
		t.Errorf("Prune removed %d entries, want 1 from the sandbox cache", result.Removed)
	}
	if _, err := os.Stat(CachedBinaryPath("sandbox-cold")); !os.IsNotExist(err) {
		t.Errorf("coldest sandbox entry survived: %v", err)
	}
}

// binaryName mirrors the platform-dependent filename CachedBinaryPath
// builds, so the assertion above reads as a layout check rather than a
// GOOS check.
func binaryName() string {
	if filepath.Ext(CachedBinaryPath("x")) == ".exe" {
		return "pipelines.exe"
	}
	return "pipelines"
}
