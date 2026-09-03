package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// A memo entry outlives its origin run's rows, so a later hit hands the
// consumer an output_ref whose node row is gone and the node fails.
func TestDeleteRun_DropsMemoEntriesFromDeletedRun(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "producer/n", RunID: "producer", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue, CacheKeyHash: "h1",
	})
	released, err := s.ReleaseConcurrencySlot(ctx, "k", "producer/n", "success", "producer/n", "h1", time.Hour)
	if err != nil || !released {
		t.Fatalf("release: released=%v err=%v", released, err)
	}
	if err := s.DeleteRun(ctx, "producer"); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}

	consumer := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "consumer/n", RunID: "consumer", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue, CacheKeyHash: "h1",
	})
	if consumer.Kind == store.AcquireCached {
		t.Fatalf("consumer got a cache hit on deleted run %s/%s; want to execute",
			consumer.OriginRunID, consumer.OriginNodeID)
	}
	if consumer.Kind != store.AcquireGranted {
		t.Fatalf("consumer: want Granted got %s", consumer.Kind)
	}
}

func TestPruneRunsOlderThan_DropsMemoEntriesFromPrunedRuns(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "producer/n", RunID: "producer", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue, CacheKeyHash: "h1",
	})
	released, err := s.ReleaseConcurrencySlot(ctx, "k", "producer/n", "success", "producer/n", "h1", time.Hour)
	if err != nil || !released {
		t.Fatalf("release: released=%v err=%v", released, err)
	}
	if err := s.FinishRun(ctx, "producer", "success", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	pruned, err := s.PruneRunsOlderThan(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("PruneRunsOlderThan: %v", err)
	}
	if len(pruned) != 1 || pruned[0] != "producer" {
		t.Fatalf("pruned = %v, want [producer]", pruned)
	}

	consumer := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "consumer/n", RunID: "consumer", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue, CacheKeyHash: "h1",
	})
	if consumer.Kind != store.AcquireGranted {
		t.Fatalf("consumer: want Granted got %s (origin %s/%s)",
			consumer.Kind, consumer.OriginRunID, consumer.OriginNodeID)
	}
}
