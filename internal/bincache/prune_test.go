package bincache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedEntry writes a fake cached binary of the given size and backdates
// its last-used stamp.
func seedEntry(t *testing.T, key string, size int, age time.Duration) string {
	t.Helper()
	path := CachedBinaryPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", key, err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o755); err != nil {
		t.Fatalf("write %s: %v", key, err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", key, err)
	}
	return path
}

// isolateCache points SPARKWING_HOME at a scratch directory so a test
// never touches the developer's real cache.
func isolateCache(t *testing.T) {
	t.Helper()
	t.Setenv("SPARKWING_HOME", t.TempDir())
}

func TestPrune_EvictsLeastRecentlyUsedToByteCeiling(t *testing.T) {
	isolateCache(t)
	seedEntry(t, "cold", 100, 72*time.Hour)
	seedEntry(t, "warm", 100, 48*time.Hour)
	seedEntry(t, "hot", 100, 24*time.Hour)

	result, err := Prune(250, 0)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Removed != 1 || result.Freed != 100 {
		t.Fatalf("expected one eviction of 100 bytes, got %+v", result)
	}
	if _, err := os.Stat(CachedBinaryPath("cold")); !os.IsNotExist(err) {
		t.Fatalf("the least recently used entry should have been evicted")
	}
	for _, keep := range []string{"warm", "hot"} {
		if _, err := os.Stat(CachedBinaryPath(keep)); err != nil {
			t.Fatalf("%s should have survived: %v", keep, err)
		}
	}
}

func TestPrune_EnforcesEntryCeiling(t *testing.T) {
	isolateCache(t)
	for i, key := range []string{"a", "b", "c", "d"} {
		seedEntry(t, key, 10, time.Duration(96-i*24)*time.Hour)
	}

	result, err := Prune(0, 2)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Removed != 2 || result.Kept != 2 {
		t.Fatalf("expected to cut four entries to two, got %+v", result)
	}
}

// Build time is not use time. An entry compiled long ago but used
// minutes ago must outrank a newer one that nothing has touched since,
// or a binary in daily use gets evicted for being old.
func TestPrune_RanksByLastUseNotBuildTime(t *testing.T) {
	isolateCache(t)
	seedEntry(t, "built-long-ago-used-often", 100, 90*24*time.Hour)
	seedEntry(t, "built-recently-never-reused", 100, 30*time.Minute)

	Touch(CachedBinaryPath("built-long-ago-used-often"))

	if _, err := Prune(150, 0); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Stat(CachedBinaryPath("built-long-ago-used-often")); err != nil {
		t.Fatalf("a touched entry must survive regardless of build age: %v", err)
	}
	if _, err := os.Stat(CachedBinaryPath("built-recently-never-reused")); !os.IsNotExist(err) {
		t.Fatalf("the untouched entry should have been evicted")
	}
}

// A run stats its binary and then execs it. Evicting inside that window
// turns a cache hit into a failure, so recent entries are off limits
// even when the cache is over its ceiling.
func TestPrune_RefusesToEvictWithinGraceWindow(t *testing.T) {
	isolateCache(t)
	seedEntry(t, "just-used-a", 100, time.Minute)
	seedEntry(t, "just-used-b", 100, 2*time.Minute)

	result, err := Prune(50, 0)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Removed != 0 {
		t.Fatalf("entries inside the grace window must not be evicted, got %+v", result)
	}
}

// A directory with no binary is debris from an interrupted compile.
func TestPrune_SweepsOrphanedEntryDirectories(t *testing.T) {
	isolateCache(t)
	seedEntry(t, "real", 100, time.Hour)
	orphan := filepath.Join(CacheRoot(), "orphan")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatalf("mkdir orphan: %v", err)
	}

	if _, err := Prune(0, 1); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("an entry directory with no binary should be swept")
	}
	if _, err := os.Stat(CachedBinaryPath("real")); err != nil {
		t.Fatalf("the real entry should have survived: %v", err)
	}
}

func TestPrune_ZeroCeilingsDisableEviction(t *testing.T) {
	isolateCache(t)
	seedEntry(t, "a", 1000, 100*time.Hour)
	seedEntry(t, "b", 1000, 200*time.Hour)

	result, err := Prune(0, 0)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Removed != 0 || result.Kept != 2 {
		t.Fatalf("zero ceilings mean unlimited, got %+v", result)
	}
}

func TestPrune_MissingCacheRootIsNotAnError(t *testing.T) {
	isolateCache(t)
	result, err := Prune(100, 1)
	if err != nil {
		t.Fatalf("a cache that was never created should scan as empty: %v", err)
	}
	if result.Considered != 0 {
		t.Fatalf("expected an empty scan, got %+v", result)
	}
}

func TestParseBytes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"1024", 1024},
		{"0", 0},
		{"512B", 512},
		{"2KiB", 2048},
		{"1MiB", 1 << 20},
		{"2GiB", 2 << 30},
		{"1KB", 1000},
		{"1MB", 1000 * 1000},
		{" 4GiB ", 4 << 30},
		{"1.5GiB", 1610612736},
	} {
		got, err := ParseBytes(tc.in)
		if err != nil {
			t.Errorf("ParseBytes(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseBytes(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", "-1", "banana", "12PB", "-2GiB"} {
		if _, err := ParseBytes(bad); err == nil {
			t.Errorf("ParseBytes(%q) should have failed", bad)
		}
	}
}

func TestConfiguredLimits_FallBackOnGarbage(t *testing.T) {
	t.Setenv(MaxCacheBytesEnv, "not-a-size")
	t.Setenv(MaxCacheEntriesEnv, "not-a-number")
	if got := ConfiguredMaxBytes(); got != DefaultMaxCacheBytes {
		t.Fatalf("unparseable byte ceiling should fall back, got %d", got)
	}
	if got := ConfiguredMaxEntries(); got != DefaultMaxCacheEntries {
		t.Fatalf("unparseable entry ceiling should fall back, got %d", got)
	}
}

func TestConfiguredLimits_HonorEnvironment(t *testing.T) {
	t.Setenv(MaxCacheBytesEnv, "512MiB")
	t.Setenv(MaxCacheEntriesEnv, "5")
	if got := ConfiguredMaxBytes(); got != 512<<20 {
		t.Fatalf("ConfiguredMaxBytes = %d, want %d", got, 512<<20)
	}
	if got := ConfiguredMaxEntries(); got != 5 {
		t.Fatalf("ConfiguredMaxEntries = %d, want 5", got)
	}
}
