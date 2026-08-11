package bincache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func seedEntry(t *testing.T, entry Entry, body string, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(entry.binaryPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.binaryPath(), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Dir(entry.binaryPath()), modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func testEntry(t *testing.T, root, key string) Entry {
	t.Helper()
	entry, err := pipelineEntryAt(root, key)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestPruneSkipsActiveHolderAndReclaimsAnotherEntry(t *testing.T) {
	root := t.TempDir()
	old := testEntry(t, root, "11111111-11111111")
	newer := testEntry(t, root, "22222222-22222222")
	seedEntry(t, old, "old", time.Unix(1, 0))
	seedEntry(t, newer, "newer", time.Unix(2, 0))

	lease, found, err := old.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !found {
		t.Fatal("seeded entry was not found")
	}
	t.Cleanup(func() {
		if err := lease.Release(); err != nil {
			t.Errorf("Release: %v", err)
		}
	})

	result, err := Prune(context.Background(), PruneOptions{
		Root:         root,
		ReclaimBytes: 1,
		MaxEntries:   2,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Active != 1 || result.Reclaimed != 1 || result.ReclaimedBytes != 5 || !result.GoalSatisfied {
		t.Fatalf("unexpected result: %+v", result)
	}
	if _, err := os.Stat(old.binaryPath()); err != nil {
		t.Fatalf("active entry removed: %v", err)
	}
	if _, err := os.Stat(newer.binaryPath()); !os.IsNotExist(err) {
		t.Fatalf("reclaimable entry remains: %v", err)
	}
}

func TestPruneIsBoundedAndUsesStableKeyOrder(t *testing.T) {
	root := t.TempDir()
	stamp := time.Unix(1, 0)
	first := testEntry(t, root, "11111111-11111111")
	second := testEntry(t, root, "22222222-22222222")
	third := testEntry(t, root, "33333333-33333333")
	seedEntry(t, third, "333", stamp)
	seedEntry(t, second, "22", stamp)
	seedEntry(t, first, "1", stamp)

	result, err := Prune(context.Background(), PruneOptions{
		Root:         root,
		ReclaimBytes: 100,
		MaxEntries:   1,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if result.Examined != 1 || result.Reclaimed != 1 || result.ObservedBytes != 1 || result.ReclaimedBytes != 1 {
		t.Fatalf("unexpected bounded accounting: %+v", result)
	}
	if !result.Exhausted || result.GoalSatisfied {
		t.Fatalf("bounded miss must be exhausted without satisfying goal: %+v", result)
	}
	if _, err := os.Stat(first.binaryPath()); !os.IsNotExist(err) {
		t.Fatalf("stable first entry remains: %v", err)
	}
	for _, entry := range []Entry{second, third} {
		if _, err := os.Stat(entry.binaryPath()); err != nil {
			t.Fatalf("unexamined entry removed: %v", err)
		}
	}
}

func TestPruneDoesNotRaceWriterOrExposePartialEntry(t *testing.T) {
	root := t.TempDir()
	entry := testEntry(t, root, "11111111-11111111")
	started := make(chan string, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := entry.Materialize(context.Background(), func(path string) error {
			if err := os.WriteFile(path, []byte("complete"), 0o755); err != nil {
				return err
			}
			started <- path
			<-release
			return nil
		})
		done <- err
	}()

	select {
	case staging := <-started:
		if staging == entry.binaryPath() {
			t.Fatal("writer received the final managed path")
		}
	case err := <-done:
		t.Fatalf("Materialize returned before writer entered: %v", err)
	case <-time.After(time.Second):
		t.Fatal("Materialize did not enter writer")
	}

	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatalf("Prune while writer active: %v", err)
	}
	if result.Busy != 1 || result.Reclaimed != 0 {
		t.Fatalf("active writer was not skipped: %+v", result)
	}
	if _, err := os.Stat(entry.binaryPath()); !os.IsNotExist(err) {
		t.Fatalf("partial final entry became visible: %v", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	body, err := os.ReadFile(entry.binaryPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "complete" {
		t.Fatalf("published body = %q", body)
	}
}

func TestEntryRejectsInvalidKeysAndPruneRejectsInvalidBounds(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"", "../escape", "AAAAAAAA-BBBBBBBB", "11111111-1111111"} {
		if _, err := pipelineEntryAt(root, key); err == nil {
			t.Errorf("pipelineEntryAt(%q) succeeded", key)
		}
	}

	for _, opts := range []PruneOptions{
		{Root: root, ReclaimBytes: 0, MaxEntries: 1},
		{Root: root, ReclaimBytes: 1, MaxEntries: 0},
	} {
		if _, err := Prune(context.Background(), opts); err == nil || errors.Is(err, context.Canceled) {
			t.Errorf("Prune(%+v) error = %v", opts, err)
		}
	}
}
