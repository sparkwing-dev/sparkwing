//go:build darwin || linux

package bincache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemovalCreditsOnlyObservedAllocatedCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entry")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "pipelines"), make([]byte, 8192), 0o700); err != nil {
		t.Fatal(err)
	}
	allocated, err := cacheAllocatedBytes(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	originalAvailable := cacheFilesystemAvailableBytes
	t.Cleanup(func() { cacheFilesystemAvailableBytes = originalAvailable })
	read := 0
	cacheFilesystemAvailableBytes = func(string) (int64, error) {
		read++
		if read == 1 {
			return 100, nil
		}
		return 100 + allocated + 1, nil
	}
	reclaimed, err := removeCacheEntryWithCapacity(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed != allocated {
		t.Fatalf("reclaimed bytes = %d, want allocated cap %d", reclaimed, allocated)
	}
}

func TestManagedRemovalDoesNotCreditUnobservedCapacity(t *testing.T) {
	originalAvailable := cacheFilesystemAvailableBytes
	t.Cleanup(func() { cacheFilesystemAvailableBytes = originalAvailable })
	cacheFilesystemAvailableBytes = func(string) (int64, error) { return 100, nil }

	root := filepath.Join(t.TempDir(), pipelineCacheSchema)
	entry := testEntry(t, root, "11111111-11111111")
	seedEntry(t, entry, "managed", time.Unix(1, 0))

	result, err := Prune(context.Background(), PruneOptions{
		Root: root, ReclaimBytes: 1, MaxEntries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReclaimedEntries != 1 {
		t.Fatalf("removed entries = %d, want 1", result.ReclaimedEntries)
	}
	if result.ObservedCapacityGainedBytes != 0 || result.GoalSatisfied {
		t.Fatalf("managed removal claimed unobserved capacity: %+v", result)
	}
	if _, err := os.Stat(entry.entryDir()); !os.IsNotExist(err) {
		t.Fatalf("managed entry remains after removal: %v", err)
	}
}
