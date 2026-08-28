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

func TestPrune_UnisolatedRunsAgainstTheSandbox(t *testing.T) {
	t.Setenv("SPARKWING_HOME", "")

	defaultRoot := CacheRoot()
	if !strings.HasPrefix(defaultRoot, os.TempDir()) {
		t.Fatalf("CacheRoot() = %q, want a path under the test sandbox", defaultRoot)
	}

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
