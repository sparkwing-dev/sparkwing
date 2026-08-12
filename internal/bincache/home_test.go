package bincache

import (
	"context"
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
		{"PipelineEntry", mustPipelineEntry(t, "deadbeef-cafebabe").binaryPath()},
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
	if got, wantRoot := CacheRoot(), filepath.Join(want, "cache", "pipelines", pipelineCacheSchema, "entries"); got != wantRoot {
		t.Errorf("CacheRoot() = %q, want %q", got, wantRoot)
	}
	entry := mustPipelineEntry(t, "deadbeef-cafebabe")
	wantBin := filepath.Join(want, "cache", "pipelines", pipelineCacheSchema, "entries", "deadbeef-cafebabe", filepath.Base(entry.binaryPath()))
	if got := entry.binaryPath(); got != wantBin {
		t.Errorf("PipelineEntry binary = %q, want %q", got, wantBin)
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

	defaultRoot := CacheRoot()
	if !strings.HasPrefix(defaultRoot, os.TempDir()) {
		t.Fatalf("CacheRoot() = %q, want a path under the test sandbox", defaultRoot)
	}
	// Isolate the destructive assertion from parallel package tests while keeping
	// it beneath the same test-owned sandbox whose default was proved above.
	t.Setenv("SPARKWING_HOME", filepath.Join(SparkwingHome(), t.Name()))
	root := CacheRoot()
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	cold := mustPipelineEntry(t, "aaaaaaaa-00000001")
	warm := mustPipelineEntry(t, "aaaaaaaa-00000002")
	seedEntry(t, cold, strings.Repeat("c", 100), time.Now().Add(-72*time.Hour))
	seedEntry(t, warm, strings.Repeat("w", 100), time.Now().Add(-48*time.Hour))

	result, err := Prune(context.Background(), PruneOptions{ReclaimEntries: 1, MaxEntries: 2})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.ReclaimedEntries != 1 {
		t.Errorf("Prune removed %d entries, want 1 from the sandbox cache", result.ReclaimedEntries)
	}
	if _, err := os.Stat(cold.binaryPath()); !os.IsNotExist(err) {
		t.Errorf("coldest sandbox entry survived: %v", err)
	}
}

func mustPipelineEntry(t *testing.T, key string) Entry {
	t.Helper()
	entry, err := PipelineEntry(key)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}
