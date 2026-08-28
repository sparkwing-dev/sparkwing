package store_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestConcurrency_PromoteOntoSupersededHolderDoesNotCrash(t *testing.T) {
	s := newStoreT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitCancelOthers,
	}); r.Kind != store.AcquireCancellingOthers {
		t.Fatalf("B: want CancellingOthers, got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("A re-arrival: want Queued (B holds, superseded row lingering), got %s", r.Kind)
	}
	promoted := releaseAndPromoteT(t, s, "k", "rB/n")
	var aPromoted bool
	for _, w := range promoted {
		if w.RunID == "rA" {
			aPromoted = true
		}
	}
	if !aPromoted {
		t.Fatalf("A was not promoted after B released; promoted=%+v", promoted)
	}
	if got := activeHolders(t, s, "k"); got != 1 {
		t.Fatalf("active holders after promotion = %d, want 1", got)
	}
}

func TestConcurrency_PromoteOntoExpiredHolderReclaimsRow(t *testing.T) {
	s := newStoreT(t)
	acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue, Lease: time.Nanosecond,
	})
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rB/n", RunID: "rB", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("B: want Granted, got %s", r.Kind)
	}
	if r := acquireT(t, s, store.AcquireSlotRequest{
		Key: "k", HolderID: "rA/n", RunID: "rA", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("A re-arrival: want Queued (B holds, expired row lingering), got %s", r.Kind)
	}
	promoted := releaseAndPromoteT(t, s, "k", "rB/n")
	var aPromoted bool
	for _, w := range promoted {
		if w.RunID == "rA" {
			aPromoted = true
		}
	}
	if !aPromoted {
		t.Fatalf("A was not promoted after B released; promoted=%+v", promoted)
	}
	if got := activeHolders(t, s, "k"); got != 1 {
		t.Fatalf("active holders after promotion = %d, want 1", got)
	}
}
