package bincache

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestPruneToLimitsCannotSatisfyGoalFromPartialDiscovery(t *testing.T) {
	originalStatus := statusForLimits
	originalPrune := pruneForLimits
	t.Cleanup(func() {
		statusForLimits = originalStatus
		pruneForLimits = originalPrune
	})
	statusForLimits = func(context.Context, string) (CacheStatus, error) {
		return CacheStatus{DiscoveryExhausted: true}, nil
	}
	pruneForLimits = func(context.Context, PruneOptions) (PruneResult, error) {
		return PruneResult{GoalSatisfied: true}, nil
	}

	result, err := PruneToLimits(context.Background(), DefaultMaxCacheBytes, DefaultMaxCacheEntries, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.GoalSatisfied || !result.WorkBoundExhausted {
		t.Fatalf("partial discovery result = %+v, want unsatisfied and exhausted", result)
	}
}

func TestPruneToLimitsUsesLogicalRemovalForCacheByteCeiling(t *testing.T) {
	originalStatus := statusForLimits
	originalPrune := pruneForLimits
	t.Cleanup(func() {
		statusForLimits = originalStatus
		pruneForLimits = originalPrune
	})
	statusForLimits = func(context.Context, string) (CacheStatus, error) {
		return CacheStatus{ObservedBytes: 100, EntryCount: 2}, nil
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
	if request.RemoveBytes != 50 || request.ReclaimBytes != 0 || request.MaxEntries != 2 {
		t.Fatalf("prune request = %+v", request)
	}
	if !result.GoalSatisfied || result.LogicalRemovedBytes != 60 {
		t.Fatalf("prune result = %+v", result)
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

func TestExecReplace_MissingBinaryReportsNotExist(t *testing.T) {
	err := ExecReplace(filepath.Join(t.TempDir(), "absent"), nil, "", os.Environ())
	if err == nil {
		t.Fatal("exec of a missing binary should fail rather than replace the process")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("compileAndExec keys its rebuild retry on fs.ErrNotExist, got %#v", err)
	}
}
