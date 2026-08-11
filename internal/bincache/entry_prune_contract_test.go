package bincache

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
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

	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
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
	entry := testEntry(t, root, "11111111-11111111")
	if _, err := enqueueCacheEntry(context.Background(), root, entry.key); err != nil {
		t.Fatal(err)
	}

	result, err := Prune(context.Background(), PruneOptions{Root: root, ReclaimBytes: 1, MaxEntries: 1})
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
		classified += value
	}
	if examined != classified {
		t.Fatalf("examined_entries = %v, classified entries = %v: %s", examined, classified, raw)
	}
	if fields["work_bound_exhausted"] != true {
		t.Fatalf("work_bound_exhausted = %#v, want true after consuming all candidates: %s",
			fields["work_bound_exhausted"], raw)
	}
}
