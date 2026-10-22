package bincache

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPruneToLimitsWithoutObservedExcess(t *testing.T) {
	originalStatus := statusForLimits
	t.Cleanup(func() { statusForLimits = originalStatus })
	for _, exhausted := range []bool{false, true} {
		statusForLimits = func(context.Context, string) (CacheStatus, error) {
			return CacheStatus{ObservedBytes: 100, EntryCount: 2, DiscoveryExhausted: exhausted}, nil
		}
		result, err := pruneToLimitsAtRoot(t.Context(), t.TempDir(), 200, 3, false)
		if err != nil {
			t.Fatalf("exhausted=%v: %v", exhausted, err)
		}
		if result.GoalSatisfied != !exhausted || result.WorkBoundExhausted != exhausted || result.LogicalRemovedBytes != 0 || result.ReclaimedEntries != 0 {
			t.Fatalf("exhausted=%v: result=%+v", exhausted, result)
		}
	}
}

func TestPruneToLimitsUsesLogicalRemovalForCacheByteCeiling(t *testing.T) {
	originalStatus := statusForLimits
	originalPrune := pruneForLimits
	t.Cleanup(func() {
		statusForLimits = originalStatus
		pruneForLimits = originalPrune
	})
	for _, exhausted := range []bool{false, true} {
		statusForLimits = func(context.Context, string) (CacheStatus, error) {
			return CacheStatus{ObservedBytes: 100, EntryCount: 2, DiscoveryExhausted: exhausted}, nil
		}
		var request PruneOptions
		pruneForLimits = func(_ context.Context, opts PruneOptions) (PruneResult, error) {
			request = opts
			return PruneResult{LogicalRemovedBytes: 60, ReclaimedEntries: 1, GoalSatisfied: true}, nil
		}

		result, err := PruneToLimits(context.Background(), 50, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if request.RemoveBytes != 50 || request.MaxEntries != 2 {
			t.Fatalf("prune request = %+v", request)
		}
		if result.GoalSatisfied != !exhausted || result.WorkBoundExhausted != exhausted || result.LogicalRemovedBytes != 60 {
			t.Fatalf("prune result = %+v", result)
		}
	}
}

func writeCacheConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", path)
	return path
}

func TestConfiguredLimits_DefaultWithoutTheSection(t *testing.T) {
	writeCacheConfig(t, "repos: {}\n")
	t.Setenv("SPARKWING_CACHE_MAX_BYTES", "1")
	got, err := ConfiguredLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got != (Limits{MaxBytes: DefaultMaxCacheBytes, MaxEntries: DefaultMaxCacheEntries}) {
		t.Fatalf("limits = %+v, want the defaults; the environment is no longer read", got)
	}
}

func TestConfiguredLimits_ReadTheCacheSection(t *testing.T) {
	writeCacheConfig(t, "cache:\n  max_bytes: 512MiB\n  max_entries: 0\n")
	got, err := ConfiguredLimits()
	if err != nil {
		t.Fatal(err)
	}
	if got != (Limits{MaxBytes: 512 << 20, MaxEntries: 0}) {
		t.Fatalf("limits = %+v, want 512MiB and a disabled entry ceiling", got)
	}
}

func TestConfiguredLimits_RefuseGarbage(t *testing.T) {
	for _, body := range []string{"cache:\n  max_bytes: lots\n", "cache:\n  max_entries: -1\n"} {
		path := writeCacheConfig(t, body)
		if _, err := ConfiguredLimits(); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("%q: error = %v, want one naming %s", body, err, path)
		}
	}
}
