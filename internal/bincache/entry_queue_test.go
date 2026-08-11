package bincache

import (
	"context"
	"strings"
	"testing"
)

func TestQueueRecoversOneInterruptedEnqueue(t *testing.T) {
	root := t.TempDir()
	if err := writeAtomicFile(cacheQueueRecordPath(root, 0), []byte("11111111-11111111\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sequence, err := enqueueCacheEntry(context.Background(), root, "22222222-22222222")
	if err != nil {
		t.Fatal(err)
	}
	if sequence != 1 {
		t.Fatalf("recovered sequence = %d, want 1", sequence)
	}
	state, err := readCacheQueueState(root)
	if err != nil {
		t.Fatal(err)
	}
	if state.Head != 0 || state.Next != 2 {
		t.Fatalf("recovered state = %+v", state)
	}
}

func TestQueueRejectsUnboundedCollisionRecovery(t *testing.T) {
	root := t.TempDir()
	for sequence, key := range []string{"11111111-11111111", "22222222-22222222"} {
		if err := writeAtomicFile(cacheQueueRecordPath(root, uint64(sequence)), []byte(key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := enqueueCacheEntry(context.Background(), root, "33333333-33333333")
	if err == nil || !strings.Contains(err.Error(), "consecutive uncommitted records") {
		t.Fatalf("collision error = %v", err)
	}
}
