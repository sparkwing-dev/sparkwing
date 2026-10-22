package bincache

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

func TestPruneCoordinatorBusyDoesNotFabricateEntrySkip(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	coordinator, acquired, err := openCacheLock(root, "prune", cacheLockExclusiveNonblock)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("failed to acquire prune coordinator")
	}
	t.Cleanup(func() { _ = errors.Join(cacheUnlock(coordinator), coordinator.Close()) })

	result, err := Prune(context.Background(), PruneOptions{Root: root, RemoveBytes: 1, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["prune_busy"] != true {
		t.Fatalf("prune_busy = %#v, want true: %s", fields["prune_busy"], raw)
	}
	if fields["work_bound_exhausted"] != false {
		t.Fatalf("work_bound_exhausted = %#v, want false: %s", fields["work_bound_exhausted"], raw)
	}
	for _, name := range []string{"examined_entries", "reclaimed_entries", "active_skipped_entries", "busy_skipped_entries"} {
		if fields[name] != float64(0) {
			t.Errorf("%s = %#v, want 0: %s", name, fields[name], raw)
		}
	}
}

func TestPruneExaminedEntriesAreExactlyClassified(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	active := testEntry(t, root, "11111111-11111111")
	busy := testEntry(t, root, "22222222-22222222")
	reclaimable := testEntry(t, root, "33333333-33333333")
	for _, entry := range []Entry{active, busy, reclaimable} {
		seedEntry(t, entry, "binary", time.Unix(1, 0))
	}
	lease, found, err := active.Acquire(context.Background())
	if err != nil || !found {
		t.Fatalf("Acquire = (%v, %v), want a seeded entry", found, err)
	}
	t.Cleanup(func() {
		if err := lease.Release(); err != nil {
			t.Error(err)
		}
	})
	writer, err := busy.openLock("writer", cacheLockExclusive)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := errors.Join(cacheUnlock(writer), writer.Close()); err != nil {
			t.Error(err)
		}
	})

	result, err := Prune(context.Background(), PruneOptions{Root: root, RemoveBytes: math.MaxInt64, MaxEntries: 4})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	examined, _ := fields["examined_entries"].(float64)
	classified := 0.0
	for _, name := range []string{"reclaimed_entries", "active_skipped_entries", "busy_skipped_entries"} {
		value, _ := fields[name].(float64)
		if value != 1 {
			t.Errorf("%s = %v, want 1: %s", name, value, raw)
		}
		classified += value
	}
	if examined != 3 || examined != classified {
		t.Fatalf("examined_entries = %v, classified entries = %v: %s", examined, classified, raw)
	}
	if fields["work_bound_exhausted"] != false {
		t.Fatalf("work_bound_exhausted = %#v, want false when inventory rather than a work bound is exhausted: %s",
			fields["work_bound_exhausted"], raw)
	}
}
