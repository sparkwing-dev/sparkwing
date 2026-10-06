//go:build windows

package bincache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func testNativeActiveLegacyWriter(t *testing.T) bool {
	t.Helper()
	previous := cacheNow
	t.Cleanup(func() { cacheNow = previous })
	now := time.Unix(100, 0)
	cacheNow = func() time.Time { return now }
	cacheRoot := t.TempDir()
	root := filepath.Join(cacheRoot, pipelineCacheSchema)
	legacy := filepath.Join(cacheRoot, "11111111-11111111")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(filepath.Join(legacy, "pipelines"), os.O_CREATE|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	prune := func() PruneResult {
		result, err := Prune(t.Context(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if _, err := writer.WriteString("before"); err != nil {
		t.Fatal(err)
	}
	if got := prune(); got.ActiveSkippedEntries != 1 || got.ReclaimedEntries != 0 {
		t.Fatalf("active writer result = %+v", got)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatal("active writer lost original path")
	}
	if _, err := writer.WriteString("after"); err != nil {
		t.Fatal("pruning interrupted active writer")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(legacyRetirementGrace)
	if got := prune(); got.ReclaimedEntries != 0 {
		t.Fatal("retirement skipped its grace period")
	}
	quarantine := filepath.Join(root, "legacy-retired", filepath.Base(legacy))
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatal("closed writer was not quarantined")
	}
	now = now.Add(legacyRetirementGrace - time.Second)
	if got := prune(); got.ReclaimedEntries != 0 {
		t.Fatal("quarantine reclaimed before grace")
	}
	now = now.Add(time.Second)
	if got := prune(); got.ReclaimedEntries != 1 {
		t.Fatalf("mature quarantine result = %+v", got)
	}
	if _, err := os.Stat(quarantine); !os.IsNotExist(err) {
		t.Fatal("mature quarantine remains")
	}
	return true
}

func TestLegacyRetirementDoesNotMistakePermissionForLiveness(t *testing.T) {
	path := t.TempDir()
	busy, err := legacyRetirementBusy(context.Background(), path, windows.ERROR_ACCESS_DENIED)
	if err != nil || busy {
		t.Fatalf("unheld directory reported busy: %v", err)
	}
}
