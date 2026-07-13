package chaos

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestCheckLedgerTruth_FlagsOverCapacity(t *testing.T) {
	qs := wingwire.QueueState{Resources: []wingwire.ResourceState{
		{Key: "deploy", Capacity: 1, Held: 2},
	}}
	if v := checkLedgerTruth(qs); len(v) != 1 {
		t.Fatalf("want 1 over-capacity violation, got %v", v)
	}
}

func TestCheckLedgerTruth_AcceptsSoundSnapshot(t *testing.T) {
	qs := wingwire.QueueState{
		Resources: []wingwire.ResourceState{{Key: "cores", Capacity: 8, Held: 3}},
		Holders:   []wingwire.Holder{{RunID: "a"}},
		Waiters:   []wingwire.Waiter{{RunID: "b"}},
	}
	if v := checkLedgerTruth(qs); len(v) != 0 {
		t.Fatalf("sound snapshot flagged: %v", v)
	}
}

func TestCheckLivenessTruth_FlagsStrandedWaiterWithNoHolders(t *testing.T) {
	qs := wingwire.QueueState{
		Resources: []wingwire.ResourceState{{Key: "cores", Capacity: 8, Held: 0}},
		Waiters:   []wingwire.Waiter{{RunID: "stranded"}},
	}
	if v := checkLivenessTruth(qs); len(v) != 1 {
		t.Fatalf("want a liveness violation for a waiter behind zero holders, got %v", v)
	}
}

func TestCheckLivenessTruth_AcceptsWaiterBehindHolder(t *testing.T) {
	qs := wingwire.QueueState{
		Resources: []wingwire.ResourceState{{Key: "cores", Capacity: 8, Held: 8}},
		Holders:   []wingwire.Holder{{RunID: "a"}},
		Waiters:   []wingwire.Waiter{{RunID: "b"}},
	}
	if v := checkLivenessTruth(qs); len(v) != 0 {
		t.Fatalf("waiter behind a real holder flagged: %v", v)
	}
}

func TestCheckLedgerTruth_FlagsHolderWaiterOverlap(t *testing.T) {
	qs := wingwire.QueueState{
		Holders: []wingwire.Holder{{RunID: "a"}},
		Waiters: []wingwire.Waiter{{RunID: "a"}},
	}
	if v := checkLedgerTruth(qs); len(v) == 0 {
		t.Fatal("expected overlap violation")
	}
}

func TestCheckLedgerTruth_FlagsDuplicateHolder(t *testing.T) {
	qs := wingwire.QueueState{Holders: []wingwire.Holder{{RunID: "a"}, {RunID: "a"}}}
	if v := checkLedgerTruth(qs); len(v) == 0 {
		t.Fatal("expected duplicate-holder violation")
	}
}

func TestCheckOSTruth_FlagsPhantomAlways(t *testing.T) {
	qs := wingwire.QueueState{Holders: []wingwire.Holder{{RunID: "ghost"}}}
	if v := checkOSTruth(qs, map[string]bool{}, map[string]bool{}, false); len(v) != 1 {
		t.Fatalf("phantom must flag regardless of stability, got %v", v)
	}
}

func TestCheckOSTruth_LeakGatedByStability(t *testing.T) {
	qs := wingwire.QueueState{Holders: []wingwire.Holder{{RunID: "a"}}}
	known := map[string]bool{"a": true}
	live := map[string]bool{}
	if v := checkOSTruth(qs, live, known, false); len(v) != 0 {
		t.Fatalf("leak must be suppressed while unstable, got %v", v)
	}
	if v := checkOSTruth(qs, live, known, true); len(v) != 1 {
		t.Fatalf("leak must flag once stable, got %v", v)
	}
}

func TestCheckConverged_RequiresEmpty(t *testing.T) {
	if v := checkConverged(wingwire.QueueState{}); len(v) != 0 {
		t.Fatalf("empty state must converge, got %v", v)
	}
	busy := wingwire.QueueState{
		Holders:   []wingwire.Holder{{RunID: "a"}},
		Resources: []wingwire.ResourceState{{Key: "cores", Held: 1}},
	}
	if v := checkConverged(busy); len(v) == 0 {
		t.Fatal("non-empty state must not converge")
	}
}
